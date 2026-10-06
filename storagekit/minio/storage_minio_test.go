package minio_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	miniogo "github.com/minio/minio-go/v7"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
	"git.stit.tech/stit-core/golibinfra/testkit/storagetest"
)

var bucketSeq atomic.Int64

// containerConfig — конфиг на живой контейнер с собственным бакетом на вызов.
func containerConfig(t *testing.T, mutate ...func(*storagekit.Config)) storagekit.Config {
	t.Helper()
	fix := minioFixture(t)

	cfg := storagekit.Config{
		Endpoint:      fix.Endpoint,
		AccessKey:     fix.AccessKey,
		SecretKey:     fix.SecretKey,
		Credentials:   storagekit.CredentialsStatic,
		Bucket:        fmt.Sprintf("test-bucket-%d", bucketSeq.Add(1)),
		Region:        "us-east-1",
		BucketLookup:  storagekit.BucketLookupAuto,
		PartSize:      16 << 20,
		MaxRetries:    1,
		ProbeTimeout:  3 * time.Second,
		URLStyle:      storagekit.URLStylePath,
		PublicBaseURL: "https://cdn.example.com",
	}
	for _, m := range mutate {
		m(&cfg)
	}
	return cfg
}

func newStorage(t *testing.T, mutate ...func(*storagekit.Config)) (*skminio.Storage, *miniogo.Client, storagekit.Config) {
	t.Helper()
	cfg := containerConfig(t, mutate...)

	client, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.MakeBucket(t.Context(), cfg.Bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	urls := storagekit.NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket, cfg.URLStyle)
	return mustStorage(t, client, cfg, urls, logkit.Nop()), client, cfg
}

func mustStorage(t *testing.T, client *miniogo.Client, cfg storagekit.Config, urls *storagekit.URLBuilder, log logkit.Logger) *skminio.Storage {
	t.Helper()
	st, err := skminio.NewStorage(client, cfg, urls, log)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	return st
}

func listKeys(t *testing.T, client *miniogo.Client, bucket string) []string {
	t.Helper()
	var keys []string
	for obj := range client.ListObjects(context.Background(), bucket, miniogo.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			t.Fatalf("list objects: %v", obj.Err)
		}
		keys = append(keys, obj.Key)
	}
	return keys
}

func TestStorage_Contract(t *testing.T) {
	storagetest.RunContract(t, func(t *testing.T) storagetest.Subject {
		st, client, cfg := newStorage(t)
		return storagetest.Subject{
			Storage:   st,
			Presigner: st,
			Keys:      func(t *testing.T) []string { return listKeys(t, client, cfg.Bucket) },
		}
	})
}

// Регрессия F1: при size < 0 minio-go без PartSize считает часть от 5 TiB и
// аллоцирует ~528 MiB на каждый вызов; Config без PartSize NewStorage отвергает.
func TestStorage_PutUnknownSizeAllocatesOnePart(t *testing.T) {
	st, client, cfg := newStorage(t)
	unset := cfg
	unset.PartSize = 0
	urls := storagekit.NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket, cfg.URLStyle)
	if _, err := skminio.NewStorage(client, unset, urls, logkit.Nop()); !errors.Is(err, storagekit.ErrConfig) {
		t.Fatalf("NewStorage without PartSize: %v, want ErrConfig", err)
	}

	subjects := map[string]*skminio.Storage{
		"configured part size": st,
	}
	for name, subject := range subjects {
		t.Run(name, func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), 100_000)

			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)

			ref, err := subject.Put(t.Context(), stream(bytes.NewReader(body)), "application/octet-stream", -1)
			if err != nil {
				t.Fatalf("Put(-1): %v", err)
			}
			runtime.ReadMemStats(&after)

			if ref.Size != int64(len(body)) {
				t.Fatalf("Size = %d, want %d", ref.Size, len(body))
			}
			const budget = 64 << 20
			if delta := after.TotalAlloc - before.TotalAlloc; delta > budget {
				t.Fatalf("Put(-1) allocated %d MiB for a %d KiB body, budget %d MiB",
					delta>>20, len(body)>>10, budget>>20)
			}
		})
	}
}

func TestStorage_PutMultipart(t *testing.T) {
	const partSize = 5 << 20
	st, _, _ := newStorage(t, func(c *storagekit.Config) { c.PartSize = partSize })
	body := bytes.Repeat([]byte("multipart-"), (partSize+1<<20)/10)

	sources := map[string]func() io.Reader{
		"seekable": func() io.Reader { return bytes.NewReader(body) },
		"stream":   func() io.Reader { return stream(bytes.NewReader(body)) },
	}
	for name, mk := range sources {
		t.Run(name, func(t *testing.T) {
			ref, err := st.Put(t.Context(), mk(), "application/octet-stream", int64(len(body)))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if ref.Size != int64(len(body)) {
				t.Fatalf("Size = %d, want %d", ref.Size, len(body))
			}

			info, err := st.Stat(t.Context(), ref.Key)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if info.Size != int64(len(body)) {
				t.Fatalf("stored size = %d, want %d", info.Size, len(body))
			}
		})
	}

	t.Run("size mismatch", func(t *testing.T) {
		_, err := st.Put(t.Context(), stream(bytes.NewReader(body)), "application/octet-stream", int64(len(body))+1)
		if !errors.Is(err, storagekit.ErrSizeMismatch) {
			t.Fatalf("Put with short multipart body: err = %v, want ErrSizeMismatch", err)
		}
	})
}

// Граница «один PUT вместо multipart» включающая: ровно PartSize байт едут
// одним запросом (ETag без суффикса частей), PartSize+1 — уже multipart. На этой
// границе держится обещание README про память в PartSize на вызов.
func TestStorage_SinglePutBoundary(t *testing.T) {
	const partSize = 5 << 20
	st, _, _ := newStorage(t, func(c *storagekit.Config) { c.PartSize = partSize })

	cases := map[string]struct {
		size          int
		wantMultipart bool
	}{
		"exactly one part": {partSize, false},
		"one byte over":    {partSize + 1, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := bytes.Repeat([]byte("b"), tc.size)
			ref, err := st.Put(t.Context(), bytes.NewReader(body), "application/octet-stream", int64(tc.size))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			info, err := st.Stat(t.Context(), ref.Key)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			// ETag multipart-объекта — «<md5>-<число частей>».
			if multipart := strings.Contains(info.ETag, "-"); multipart != tc.wantMultipart {
				t.Fatalf("size %d: ETag %q says multipart=%v, want %v", tc.size, info.ETag, multipart, tc.wantMultipart)
			}
		})
	}
}

// Источник может объявлять io.Seeker и не уметь позиционироваться (тело поверх
// сети): пока позиция цела, кит грузит потоком. Если вернуть позицию не удалось,
// источник уже стоит в конце — такой Put отвергается, а не кладёт пустой объект.
func TestStorage_PutFromFailingSeeker(t *testing.T) {
	st, client, cfg := newStorage(t)
	body := []byte("failing seeker body")

	for failOn := 1; failOn <= 2; failOn++ {
		t.Run(fmt.Sprintf("streams when seek fails on call %d", failOn), func(t *testing.T) {
			r := &flakySeeker{Reader: bytes.NewReader(body), failOn: failOn}
			ref, err := st.Put(t.Context(), r, "application/octet-stream", int64(len(body)))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			info, err := st.Stat(t.Context(), ref.Key)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if info.Size != int64(len(body)) {
				t.Fatalf("stored size = %d, want %d", info.Size, len(body))
			}
		})
	}

	t.Run("rejects a source it cannot rewind", func(t *testing.T) {
		before := len(listKeys(t, client, cfg.Bucket))
		r := &flakySeeker{Reader: bytes.NewReader(body), failOn: 3}

		_, err := st.Put(t.Context(), r, "application/octet-stream", int64(len(body)))
		if !errors.Is(err, storagekit.ErrUploadFailed) {
			t.Fatalf("Put = %v, want ErrUploadFailed", err)
		}
		if errors.Is(err, storagekit.ErrSizeMismatch) {
			t.Fatalf("Put = %v, want the rewind failure, not a size mismatch", err)
		}
		if after := len(listKeys(t, client, cfg.Bucket)); after != before {
			t.Fatalf("bucket gained %d objects from a rejected upload", after-before)
		}
	})
}

// Регрессия F2: расхождение size ретраилось minio-go как сетевой сбой — десять
// обречённых PUT и секунды латентности на детерминированную ошибку клиента.
func TestStorage_SizeMismatchFailsFast(t *testing.T) {
	st, client, cfg := newStorage(t)
	body := []byte("0123456789")

	start := time.Now()
	for _, declared := range []int64{4, 40} {
		for name, r := range map[string]io.Reader{
			"seekable": bytes.NewReader(body),
			"stream":   stream(bytes.NewReader(body)),
		} {
			if _, err := st.Put(t.Context(), r, "application/pdf", declared); !errors.Is(err, storagekit.ErrSizeMismatch) {
				t.Fatalf("%s: Put(size=%d): err = %v, want ErrSizeMismatch", name, declared, err)
			}
		}
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("four rejected uploads took %s: size mismatch is being retried", elapsed)
	}
	if keys := listKeys(t, client, cfg.Bucket); len(keys) != 0 {
		t.Fatalf("bucket must stay empty after rejected uploads, got %v", keys)
	}
}

// Регрессия F10: объект, отвергнутый по хвосту, удаляется даже если ctx запроса
// уже отменён — иначе обрезанное тело остаётся в бакете и стоит денег. Хвост,
// прочитанный уже после отмены, тоже считается хвостом.
func TestStorage_CleanupRunsOnCancelledContext(t *testing.T) {
	delays := map[string]time.Duration{
		"tail arrives with the cancel":  0,
		"tail arrives after the cancel": 10 * time.Millisecond,
	}
	for name, delay := range delays {
		t.Run(name, func(t *testing.T) {
			st, client, cfg := newStorage(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			body := &cancelOnTail{r: strings.NewReader("0123456789"), after: 4, cancel: cancel, delay: delay}
			_, err := st.Put(ctx, body, "application/pdf", 4)
			if !errors.Is(err, storagekit.ErrSizeMismatch) {
				t.Fatalf("Put: err = %v, want ErrSizeMismatch", err)
			}
			if keys := listKeys(t, client, cfg.Bucket); len(keys) != 0 {
				t.Fatalf("rejected upload left objects behind: %v", keys)
			}
		})
	}
}

// Ключ, который отвергает сам minio-go (S3 держит предел в 1024 символа),
// приходит вызывающему как ошибка операции, а не как паника или пустой результат.
func TestStorage_RejectsOversizedKey(t *testing.T) {
	st, _, _ := newStorage(t)
	key := strings.Repeat("k", 1025)

	if _, _, err := st.Get(t.Context(), key); !errors.Is(err, storagekit.ErrGetFailed) {
		t.Fatalf("Get = %v, want ErrGetFailed", err)
	}
	if _, err := st.PresignGet(t.Context(), key, time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Fatalf("PresignGet = %v, want ErrPresignFailed", err)
	}
	if _, err := st.PresignPut(t.Context(), key, time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Fatalf("PresignPut = %v, want ErrPresignFailed", err)
	}
}

// Отмена ctx ограничивает ожидание хвоста: источник, не вернувший EOF за
// отведённое окно, считается прочитанным до конца, и объект остаётся как
// заявлено — цена контракта «reader обязан вернуть EOF после size байт».
func TestStorage_SlowTailAfterCancelKeepsObject(t *testing.T) {
	st, client, cfg := newStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	body := &cancelOnTail{r: strings.NewReader("0123456789"), after: 4, cancel: cancel, delay: 200 * time.Millisecond}
	ref, err := st.Put(ctx, body, "application/pdf", 4)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Size != 4 {
		t.Fatalf("Size = %d, want 4", ref.Size)
	}
	if keys := listKeys(t, client, cfg.Bucket); len(keys) != 1 {
		t.Fatalf("bucket keys = %v, want the object declared as 4 bytes", keys)
	}
}

func TestStorage_EmitsMetrics(t *testing.T) {
	before := metrictest.Collect(t)
	st, _, _ := newStorage(t)
	ctx := t.Context()

	ref, err := st.Put(ctx, bytes.NewReader([]byte("m")), "image/png", 1)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, _, err := st.Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatalf("read object: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := st.Stat(ctx, ref.Key); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if _, err := st.PresignGet(ctx, ref.Key, time.Minute); err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if _, err := st.PresignPut(ctx, ref.Key, time.Minute); err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if err := st.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Stat(ctx, "missing.png"); err == nil {
		t.Fatal("Stat of a missing key must fail")
	}
	d := metrictest.Collect(t).Sub(before)

	for _, k := range [][2]string{{"put", "ok"}, {"get", "ok"}, {"stat", "ok"}, {"presign_get", "ok"}, {"presign_put", "ok"}, {"delete", "ok"}, {"stat", "error"}} {
		if got := d.Get(t, "storage_ops_total", "op", k[0], "result", k[1]); got.Kind != metrictest.Counter || got.Value != 1 {
			t.Errorf("storage_ops_total: %v (%s), want counter delta 1", got, got.Kind)
		}
	}
	// F7: дефолтные бакеты SDK (мс) для метрики в секундах кладут всё в одну корзину.
	wantBounds := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
	for _, op := range []string{"put", "get", "stat", "delete", "presign_get", "presign_put"} {
		dur := d.Get(t, "storage_op_duration_seconds", "op", op)
		if dur.Kind != metrictest.Histogram || dur.Unit != "s" || dur.Count == 0 || !slices.Equal(dur.Bounds, wantBounds) {
			t.Errorf("storage_op_duration_seconds: %v (%s, unit %q, bounds %v), want histogram in s on the common buckets", dur, dur.Kind, dur.Unit, dur.Bounds)
		}
	}
}

// stream прячет Seeker/ReaderAt: источник, длину которого нельзя узнать заранее.
func stream(r io.Reader) io.Reader { return struct{ io.Reader }{r} }

// flakySeeker заявляет io.Seeker, но failOn-й вызов Seek возвращает ошибку.
type flakySeeker struct {
	*bytes.Reader
	failOn int
	calls  int
}

func (f *flakySeeker) Seek(offset int64, whence int) (int64, error) {
	f.calls++
	if f.calls == f.failOn {
		return 0, errors.New("seek is not supported by this source")
	}
	return f.Reader.Seek(offset, whence)
}

type cancelOnTail struct {
	r      io.Reader
	after  int64
	read   int64
	cancel context.CancelFunc
	// delay задерживает хвост после отмены: проверка хвоста должна дождаться
	// уже запущенного чтения, а не считать отменённый ctx отсутствием хвоста.
	delay time.Duration
}

func (c *cancelOnTail) Read(p []byte) (int, error) {
	if c.read >= c.after {
		c.cancel()
		time.Sleep(c.delay)
	}
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}
