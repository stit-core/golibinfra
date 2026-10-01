package minio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	miniogo "github.com/minio/minio-go/v7"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
)

const (
	// cleanupTimeout — окно на удаление объекта, отвергнутого по расхождению size:
	// ctx запроса к этому моменту может быть уже отменён.
	cleanupTimeout = 5 * time.Second
	// tailProbeGrace — сколько ждать уже запущенное чтение хвоста после отмены ctx.
	tailProbeGrace = 50 * time.Millisecond
)

type instruments struct {
	ops        metric.Int64Counter
	opDuration metric.Float64Histogram
}

// newInstruments зовётся в NewStorage: инструмент берёт текущий глобальный
// провайдер, а не первый провайдер процесса.
func newInstruments() *instruments {
	m := otelkit.Meter("storagekit")
	return &instruments{
		ops: otelkit.Must(m.Int64Counter("storage_ops_total",
			metric.WithDescription("Object storage operations by result"),
		)),
		opDuration: otelkit.Must(m.Float64Histogram("storage_op_duration_seconds",
			metric.WithDescription("Object storage operation duration"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
	}
}

func (i *instruments) observe(ctx context.Context, op string, start time.Time, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	i.opDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("op", op)))
	i.ops.Add(ctx, 1, metric.WithAttributes(attribute.String("op", op), attribute.String("result", result)))
}

// Storage — адаптер storagekit.Storage и storagekit.Presigner поверх minio-go.
type Storage struct {
	client   *miniogo.Client
	bucket   string
	partSize uint64
	urls     *storagekit.URLBuilder
	log      logkit.Logger
	instr    *instruments
}

// NewStorage собирает адаптер; nil-логгер заменяется на logkit.Nop.
func NewStorage(client *miniogo.Client, cfg storagekit.Config, urls *storagekit.URLBuilder, log logkit.Logger) *Storage {
	if log == nil {
		log = logkit.Nop()
	}
	partSize := cfg.PartSize
	if partSize == 0 {
		// Без явной части minio-go считает её от 5 TiB и берёт ~528 MiB буфер.
		partSize = storagekit.DefaultPartSize
	}
	return &Storage{
		client:   client,
		bucket:   cfg.Bucket,
		partSize: partSize,
		urls:     urls,
		log:      log,
		instr:    newInstruments(),
	}
}

// Put стримит тело: память на вызов ограничена PartSize (буфер одной части),
// тело целиком не буферизуется. Расхождение с заявленным size — ErrSizeMismatch,
// объект не остаётся.
func (s *Storage) Put(ctx context.Context, r io.Reader, contentType string, size int64) (ref storagekit.Ref, err error) {
	start := time.Now()
	defer func() { s.instr.observe(ctx, "put", start, err) }()

	contentType = storagekit.NormalizeContentType(contentType)
	key, err := storagekit.NewKey(contentType)
	if err != nil {
		return storagekit.Ref{}, err
	}
	opts := miniogo.PutObjectOptions{ContentType: contentType, PartSize: s.partSize}

	if size < 0 {
		info, putErr := s.client.PutObject(ctx, s.bucket, key, r, -1, opts)
		if putErr != nil {
			return storagekit.Ref{}, fmt.Errorf("%w: put object: %w", storagekit.ErrUploadFailed, putErr)
		}
		return s.ref(key, contentType, info.Size), nil
	}

	actual, known, err := remainingLen(r)
	if err != nil {
		return storagekit.Ref{}, err
	}
	if known {
		if actual != size {
			return storagekit.Ref{}, sizeMismatch(size, actual)
		}
		info, putErr := s.client.PutObject(ctx, s.bucket, key, r, size, opts)
		if putErr != nil {
			return storagekit.Ref{}, fmt.Errorf("%w: put object: %w", storagekit.ErrUploadFailed, putErr)
		}
		return s.ref(key, contentType, info.Size), nil
	}

	// Длина источника неизвестна заранее: наверх уходит ровно size байт, хвост
	// проверяется отдельным чтением после успешной загрузки.
	putCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	body := &exactReader{r: io.LimitReader(r, size), want: size}
	if uint64(size) <= s.partSize {
		// Отмена обрывает единственный PUT; у multipart она отняла бы у minio-go
		// шанс корректно снять начатую загрузку.
		body.onShort = cancel
	}

	info, putErr := s.client.PutObject(putCtx, s.bucket, key, body, size, opts)
	if putErr != nil {
		if body.truncated() {
			return storagekit.Ref{}, sizeMismatch(size, body.count())
		}
		return storagekit.Ref{}, fmt.Errorf("%w: put object: %w", storagekit.ErrUploadFailed, putErr)
	}
	if hasTail(ctx, r) {
		s.cleanup(ctx, key)
		return storagekit.Ref{}, fmt.Errorf("%w: %w: declared %d, body is longer", storagekit.ErrUploadFailed, storagekit.ErrSizeMismatch, size)
	}
	return s.ref(key, contentType, info.Size), nil
}

// Get открывает объект; вызывающий обязан закрыть io.ReadCloser.
func (s *Storage) Get(ctx context.Context, key string) (rc io.ReadCloser, info storagekit.ObjectInfo, err error) {
	start := time.Now()
	defer func() { s.instr.observe(ctx, "get", start, err) }()

	if key == "" {
		return nil, storagekit.ObjectInfo{}, fmt.Errorf("%w: key required", storagekit.ErrGetFailed)
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, miniogo.GetObjectOptions{})
	if err != nil {
		return nil, storagekit.ObjectInfo{}, wrapObjectErr(storagekit.ErrGetFailed, key, err)
	}
	// GetObject ленив: ошибка (в том числе NoSuchKey) приходит только на Stat/Read.
	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, storagekit.ObjectInfo{}, wrapObjectErr(storagekit.ErrGetFailed, key, err)
	}
	return obj, objectInfo(stat), nil
}

// Stat возвращает метаданные объекта; отсутствующий ключ — ErrNotFound.
func (s *Storage) Stat(ctx context.Context, key string) (info storagekit.ObjectInfo, err error) {
	start := time.Now()
	defer func() { s.instr.observe(ctx, "stat", start, err) }()

	if key == "" {
		return storagekit.ObjectInfo{}, fmt.Errorf("%w: key required", storagekit.ErrStatFailed)
	}
	stat, err := s.client.StatObject(ctx, s.bucket, key, miniogo.StatObjectOptions{})
	if err != nil {
		return storagekit.ObjectInfo{}, wrapObjectErr(storagekit.ErrStatFailed, key, err)
	}
	return objectInfo(stat), nil
}

// Delete идемпотентен: S3 отвечает 204 и на отсутствующий ключ.
func (s *Storage) Delete(ctx context.Context, key string) (err error) {
	start := time.Now()
	defer func() { s.instr.observe(ctx, "delete", start, err) }()

	if key == "" {
		return fmt.Errorf("%w: key required", storagekit.ErrDeleteFailed)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, miniogo.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("%w: remove object %q: %w", storagekit.ErrDeleteFailed, key, err)
	}
	return nil
}

// PresignGet подписывает GET-ссылку на объект сконфигуренного бакета. Операция
// локальная (HMAC над кредами), если у клиента задан Region — иначе minio-go
// разово резолвит bucket-location.
func (s *Storage) PresignGet(ctx context.Context, key string, ttl time.Duration, opts ...storagekit.PresignOption) (link string, err error) {
	start := time.Now()
	defer func() { s.instr.observe(ctx, "presign_get", start, err) }()

	if err := s.validatePresign(key, ttl); err != nil {
		return "", err
	}
	params := storagekit.NewPresignParams(opts...)
	reqParams := url.Values{}
	if params.ContentDisposition != "" {
		reqParams.Set("response-content-disposition", params.ContentDisposition)
	}
	if params.ContentType != "" {
		reqParams.Set("response-content-type", params.ContentType)
	}

	signed, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, reqParams)
	if err != nil {
		return "", fmt.Errorf("%w: %q/%q: %w", storagekit.ErrPresignFailed, s.bucket, key, err)
	}
	return signed.String(), nil
}

// PresignPut подписывает PUT-ссылку: клиент загружает объект напрямую в S3.
func (s *Storage) PresignPut(ctx context.Context, key string, ttl time.Duration) (link string, err error) {
	start := time.Now()
	defer func() { s.instr.observe(ctx, "presign_put", start, err) }()

	if err := s.validatePresign(key, ttl); err != nil {
		return "", err
	}
	signed, err := s.client.PresignedPutObject(ctx, s.bucket, key, ttl)
	if err != nil {
		return "", fmt.Errorf("%w: %q/%q: %w", storagekit.ErrPresignFailed, s.bucket, key, err)
	}
	return signed.String(), nil
}

func (s *Storage) validatePresign(key string, ttl time.Duration) error {
	if key == "" {
		return fmt.Errorf("%w: key required", storagekit.ErrPresignFailed)
	}
	return storagekit.ValidatePresignTTL(ttl)
}

func (s *Storage) ref(key, contentType string, size int64) storagekit.Ref {
	return storagekit.Ref{
		Key:         key,
		URL:         s.urls.Build(key),
		ContentType: contentType,
		Size:        size,
	}
}

func (s *Storage) cleanup(ctx context.Context, key string) {
	// Отвергнутый объект удаляется даже если ctx запроса уже отменён.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	if err := s.client.RemoveObject(ctx, s.bucket, key, miniogo.RemoveObjectOptions{}); err != nil {
		s.log.Error(ctx, "storagekit: rejected upload left behind",
			"bucket", s.bucket, "key", key, "error", err.Error())
	}
}

func objectInfo(stat miniogo.ObjectInfo) storagekit.ObjectInfo {
	return storagekit.ObjectInfo{
		Size:         stat.Size,
		ContentType:  stat.ContentType,
		ETag:         stat.ETag,
		LastModified: stat.LastModified,
	}
}

func wrapObjectErr(sentinel error, key string, err error) error {
	if isNotFound(err) {
		return fmt.Errorf("%w: %w: %q", sentinel, storagekit.ErrNotFound, key)
	}
	return fmt.Errorf("%w: %q: %w", sentinel, key, err)
}

func isNotFound(err error) bool {
	var resp miniogo.ErrorResponse
	if !errors.As(err, &resp) {
		return false
	}
	return resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket"
}

func sizeMismatch(declared, actual int64) error {
	return fmt.Errorf("%w: %w: declared %d, actual %d",
		storagekit.ErrUploadFailed, storagekit.ErrSizeMismatch, declared, actual)
}

// remainingLen — длина остатка seekable-источника: расхождение с заявленным
// size ловится до единственного PUT, а не ретраями обречённого запроса. Ошибка
// возвращается только когда позицию уже не вернуть — такой источник читается с
// конца и дал бы пустой объект вместо тела.
func remainingLen(r io.Reader) (int64, bool, error) {
	s, ok := r.(io.Seeker)
	if !ok {
		return 0, false, nil
	}
	cur, end, ok := seekBounds(s)
	if !ok {
		return 0, false, nil
	}
	if _, err := s.Seek(cur, io.SeekStart); err != nil {
		return 0, false, fmt.Errorf("%w: rewind source: %w", storagekit.ErrUploadFailed, err)
	}
	return end - cur, true, nil
}

// seekBounds — текущая позиция и конец источника; ok=false означает «длину
// заранее не узнать», а не сбой: такой источник грузится потоком.
func seekBounds(s io.Seeker) (cur, end int64, ok bool) {
	cur, err := s.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, 0, false
	}
	end, err = s.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, 0, false
	}
	return cur, end, true
}

// hasTail читает один байт сверх заявленного размера, не блокируя отмену ctx.
func hasTail(ctx context.Context, r io.Reader) bool {
	found := make(chan bool, 1)
	go func() {
		var probe [1]byte
		n, _ := io.ReadFull(r, probe[:])
		found <- n > 0
	}()

	select {
	case tail := <-found:
		return tail
	case <-ctx.Done():
	}
	// Отмена не должна прятать уже прочитанный хвост: иначе обрезанный объект
	// остался бы в бакете как успешная загрузка.
	select {
	case tail := <-found:
		return tail
	case <-time.After(tailProbeGrace):
		return false
	}
}

// exactReader отдаёт не больше want байт и помечает преждевременный EOF: тело
// короче заявленного — ошибка клиента, а не сетевой сбой. onShort отменяет ctx
// (для minio-go отмена неретраябельна) и потому ставится только однопартовому PUT.
type exactReader struct {
	r       io.Reader
	want    int64
	onShort func()

	read  atomic.Int64
	short atomic.Bool
}

func (e *exactReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	total := e.read.Add(int64(n))
	if errors.Is(err, io.EOF) && total < e.want {
		e.short.Store(true)
		if e.onShort != nil {
			e.onShort()
		}
	}
	return n, err
}

func (e *exactReader) truncated() bool { return e.short.Load() }

func (e *exactReader) count() int64 { return e.read.Load() }
