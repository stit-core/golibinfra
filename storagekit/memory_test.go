package storagekit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/storagekit"
	"git.stit.tech/stit-core/golibinfra/testkit/storagetest"
)

var (
	_ storagekit.Storage   = (*storagekit.MemoryStorage)(nil)
	_ storagekit.Presigner = (*storagekit.MemoryStorage)(nil)
)

func newMem() *storagekit.MemoryStorage {
	return storagekit.NewMemoryStorage(storagekit.NewURLBuilder("https://cdn.example.com", "media", storagekit.URLStylePath))
}

func TestMemoryStorage_Contract(t *testing.T) {
	storagetest.RunContract(t, func(t *testing.T) storagetest.Subject {
		m := newMem()
		return storagetest.Subject{
			Storage:   m,
			Presigner: m,
			Keys:      func(*testing.T) []string { return storagekit.MemoryKeys(m) },
		}
	})
}

func TestMemoryStorage_PutReturnsStableRef(t *testing.T) {
	m := newMem()
	body := []byte("hello-bytes")

	ref, err := m.Put(context.Background(), bytes.NewReader(body), "image/png", int64(len(body)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if want := "https://cdn.example.com/media/" + ref.Key; ref.URL != want {
		t.Errorf("URL = %q, want %q", ref.URL, want)
	}
	if !strings.HasSuffix(ref.Key, ".png") {
		t.Errorf("Key = %q, want .png suffix", ref.Key)
	}

	got, ok := storagekit.MemoryBytes(m, ref.Key)
	if !ok || !bytes.Equal(got, body) {
		t.Fatalf("Bytes(%q) = %q,%v want %q,true", ref.Key, got, ok, body)
	}
}

// Регрессия: тест-двойник отдавал внутренний слайс — мутация в тесте меняла
// «хранилище», чего боевой адаптер не делает.
func TestMemoryStorage_GetReturnsCopy(t *testing.T) {
	m := newMem()
	body := []byte("original")
	ref, err := m.Put(context.Background(), bytes.NewReader(body), "text/plain", int64(len(body)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, _, err := m.Get(context.Background(), ref.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if cerr := rc.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	copy(got, "mutated!")

	stored, _ := storagekit.MemoryBytes(m, ref.Key)
	if !bytes.Equal(stored, body) {
		t.Fatalf("Get returned internal slice: stored %q", stored)
	}
	copy(stored, "mutated!")
	if again, _ := storagekit.MemoryBytes(m, ref.Key); !bytes.Equal(again, body) {
		t.Fatalf("Bytes returned internal slice: stored %q", again)
	}
}

// Регрессия: zero value паниковал на nil-map, а NewMemoryStorage(nil) — на nil URLBuilder.
func TestMemoryStorage_ZeroValueUsable(t *testing.T) {
	for name, m := range map[string]*storagekit.MemoryStorage{
		"zero value":  {},
		"nil builder": storagekit.NewMemoryStorage(nil),
	} {
		t.Run(name, func(t *testing.T) {
			ref, err := m.Put(context.Background(), strings.NewReader("body"), "text/plain", 4)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if ref.URL != ref.Key {
				t.Errorf("URL = %q, want key %q", ref.URL, ref.Key)
			}
			if _, err := m.Stat(context.Background(), ref.Key); err != nil {
				t.Fatalf("Stat: %v", err)
			}
		})
	}
}

func TestMemoryStorage_PresignIsDeterministic(t *testing.T) {
	m := newMem()
	ctx := context.Background()

	link, err := m.PresignGet(ctx, "obj.ogg", 30*time.Second)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	want := "https://cdn.example.com/media/obj.ogg?x-presigned-method=GET&x-presigned-ttl=30"
	if link != want {
		t.Errorf("link = %q, want %q", link, want)
	}
	again, err := m.PresignGet(ctx, "obj.ogg", 30*time.Second)
	if err != nil || again != link {
		t.Errorf("presign is not deterministic: %q vs %q (err %v)", again, link, err)
	}
}

func TestMemoryStorage_KeysListsStoredObjects(t *testing.T) {
	m := newMem()
	ctx := context.Background()

	if got := storagekit.MemoryKeys(m); len(got) != 0 {
		t.Fatalf("Keys on empty storage = %v", got)
	}
	ref, err := m.Put(ctx, strings.NewReader("a"), "text/plain", 1)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := storagekit.MemoryKeys(m); len(got) != 1 || got[0] != ref.Key {
		t.Fatalf("Keys = %v, want [%s]", got, ref.Key)
	}
	if _, ok := storagekit.MemoryBytes(m, "missing"); ok {
		t.Error("Bytes(missing) reported a stored object")
	}
	if err := m.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := storagekit.MemoryKeys(m); len(got) != 0 {
		t.Fatalf("Keys after Delete = %v", got)
	}
}

// Ошибка источника — ошибка Put, а не пустой объект в сторе.
func TestMemoryStorage_PutPropagatesReadError(t *testing.T) {
	m := newMem()
	want := errors.New("source is gone")

	_, err := m.Put(context.Background(), errReader{err: want}, "text/plain", -1)
	if !errors.Is(err, storagekit.ErrUploadFailed) || !errors.Is(err, want) {
		t.Fatalf("Put = %v, want ErrUploadFailed wrapping the source error", err)
	}
	if got := storagekit.MemoryKeys(m); len(got) != 0 {
		t.Fatalf("failed Put left objects behind: %v", got)
	}
}

// Отмена во время чтения тела прекращает Put: успех означал бы объект,
// сохранённый уже после отказа вызывающего.
func TestMemoryStorage_PutHonoursCancelDuringRead(t *testing.T) {
	m := newMem()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	body := &cancelOnRead{r: strings.NewReader("payload"), cancel: cancel}
	_, err := m.Put(ctx, body, "text/plain", -1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v, want context.Canceled", err)
	}
	if got := storagekit.MemoryKeys(m); len(got) != 0 {
		t.Fatalf("cancelled Put left objects behind: %v", got)
	}
}

// Presign у in-memory реализации уважает ctx так же, как остальные операции.
func TestMemoryStorage_PresignHonoursCancelledContext(t *testing.T) {
	m := newMem()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := m.PresignGet(ctx, "obj.ogg", time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Fatalf("PresignGet with a cancelled ctx = %v, want ErrPresignFailed", err)
	}
	if _, err := m.PresignPut(ctx, "obj.ogg", time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Fatalf("PresignPut with a cancelled ctx = %v, want ErrPresignFailed", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// cancelOnRead отменяет ctx ровно тогда, когда тело уже читается.
type cancelOnRead struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (c *cancelOnRead) Read(p []byte) (int, error) {
	c.cancel()
	return c.r.Read(p)
}

func TestMemoryStorage_PutRejectsSizeMismatch(t *testing.T) {
	m := newMem()
	body := []byte("0123456789")

	for _, declared := range []int64{4, 40} {
		_, err := m.Put(context.Background(), bytes.NewReader(body), "application/pdf", declared)
		if !errors.Is(err, storagekit.ErrSizeMismatch) {
			t.Fatalf("Put(size=%d): err = %v, want ErrSizeMismatch", declared, err)
		}
	}
	if got := storagekit.MemoryKeys(m); len(got) != 0 {
		t.Fatalf("nothing must be stored on mismatch, got %v", got)
	}
}
