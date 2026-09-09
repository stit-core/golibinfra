# storagekit

S3-совместимое объектное хранилище: порт `Storage` (`Put`/`Get`/`Stat`/`Delete`),
presigned-ссылки (`Presigner`), построитель публичного URL (`URLBuilder`),
генератор ключей `NewKey` и две реализации — боевая (подпакет `minio` поверх
MinIO/S3) и in-memory (для тестов потребителя).

Kit доменно-нейтрален: стримит байты как есть. Image/file-семантика — валидация
mime, ограничение размера, декод картинки — на стороне приложения.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/storagekit"
import "git.stit.tech/stit-core/golibinfra/storagekit/minio" // боевой адаптер
```

Версия модуля и настройка доступа к приватной Gitea — в корневом
[`../README.md`](../README.md).

## Быстрый старт

### fx-проводка

`storagekit.Module` даёт `*URLBuilder` из `Config`. Боевые `Storage` и
`Presigner` регистрирует подпакет `storagekit/minio` своим `Module`; ему нужен
`logkit.Logger` из графа. `Config` потребитель регистрирует сам (та же
асимметрия, что у `cachekit`/`pgkit`/`natskit`).

```go
import (
    "go.uber.org/fx"

    "git.stit.tech/stit-core/golibinfra/healthkit"
    "git.stit.tech/stit-core/golibinfra/logkit"
    "git.stit.tech/stit-core/golibinfra/storagekit"
    skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
)

fx.New(
    logkit.Module,                      // → logkit.Logger
    fx.Provide(logkit.NewConfig),
    fx.Provide(storagekit.NewConfig),   // Config из ENV (STORAGE_S3_*)
    storagekit.Module,                  // → *storagekit.URLBuilder
    skminio.Module,                     // → storagekit.Storage, storagekit.Presigner
    healthkit.Provide(skminio.Probe),   // проба бакета в /ready
)
```

`skminio.Module` при `STORAGE_S3_PROBE_ON_START=true` проверяет бакет в `OnStart`:
неверный endpoint, креды или отсутствующий бакет валят старт приложения, а не
первый запрос.

### Put / Get / Stat / Delete

```go
func handle(ctx context.Context, st storagekit.Storage, body io.Reader, size int64) error {
    ref, err := st.Put(ctx, body, "image/png", size)
    if err != nil {
        return err // errors.Is(err, storagekit.ErrSizeMismatch) при расхождении size
    }
    // ref.Key — `<uuidv7>.png`; ref.URL — публичный стабильный URL; ref.Size — реальный размер

    rc, info, err := st.Get(ctx, ref.Key)
    if err != nil {
        return err // errors.Is(err, storagekit.ErrNotFound), если объекта нет
    }
    defer rc.Close()
    _ = info // ObjectInfo{Size, ContentType, ETag, LastModified}

    return st.Delete(ctx, ref.Key) // идемпотентен: отсутствующий ключ — не ошибка
}
```

`size >= 0` — точная длина тела: тело короче или длиннее → `ErrSizeMismatch`
(в обёртке `ErrUploadFailed`), объект не остаётся. `size < 0` — длина неизвестна:
тело стримится до EOF, `Ref.Size` — фактический размер.

### Presigned-ссылки

```go
link, err := pre.PresignGet(ctx, ref.Key, 15*time.Minute,
    storagekit.WithResponseContentDisposition(`attachment; filename="report.pdf"`),
    storagekit.WithResponseContentType("application/pdf"))

upload, err := pre.PresignPut(ctx, "inbox/"+ref.Key, 15*time.Minute)
```

Подписывается объект сконфигуренного бакета (`STORAGE_S3_BUCKET`). `ttl` — в
диапазоне [1 s, 7 d], иначе `ErrPresignFailed`. Опции `With*` кладут в ссылку
response-headers, которые S3 вернёт при скачивании.

### URLBuilder и NewKey

```go
urls := storagekit.NewURLBuilder("https://cdn.example.com", "media", storagekit.URLStylePath)
url := urls.Build("2026/07/abc.png") // https://cdn.example.com/media/2026/07/abc.png

key, err := storagekit.NewKey("image/png") // "<uuidv7>.png"
```

`Build` собирает `<base>/<bucket>/<key>` (`URLStylePath`) или `<base>/<key>`
(`URLStyleVirtual` — бакет уже в хосте base URL либо за CDN), схлопывая хвостовые
`/` у base и экранируя каждый сегмент ключа. `NewKey` выводит расширение из
`contentType`: явная таблица частых типов (`image/jpeg`→`.jpg`, `image/png`,
`image/webp`, `image/gif`, `image/svg+xml`, `image/avif`, `application/pdf`,
`application/json`, `application/zip`, `application/xml`, `audio/mpeg`,
`audio/ogg`, `audio/wav`, `audio/webm`, `video/mp4`, `video/webm`, `text/plain`,
`text/csv`, `text/html`), затем mime-база ОС (`mime.ExtensionsByType`);
`application/octet-stream` и неизвестный тип → ключ без расширения.

### in-memory адаптер (тесты)

```go
mem := storagekit.NewMemoryStorage(storagekit.NewURLBuilder("https://cdn.example.com", "media", storagekit.URLStylePath))
ref, _ := mem.Put(ctx, bytes.NewReader(data), "image/png", int64(len(data)))
got, ok := mem.Bytes(ref.Key) // тест-хелпер: копия сохранённых байтов
keys := mem.Keys()            // тест-хелпер: все ключи
```

`MemoryStorage` реализует `Storage` и `Presigner` и проходит тот же контрактный
набор, что боевой адаптер (см. § Тестирование). Zero value пригоден:
`storagekit.MemoryStorage{}` работает, `Ref.URL` тогда — сам ключ.

## Конфигурация

`storagekit.Config` — **part of public API** (см. `../README.md § Стабильность
API`). Менять имена/типы полей или их `envconfig`-теги = breaking change:
выпускается minor-релизом, помечается `!` в Conventional Commit и строкой
миграции в аннотации тега (см. [корневой README § Версионирование](../README.md)).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `STORAGE_S3_ENDPOINT` | `Endpoint` | `string` | — | `host[:port]` или `scheme://host[:port]` (per-concern endpoint) |
| `STORAGE_S3_ACCESS_KEY` | `AccessKey` | `string` | — | Access key (при `STORAGE_S3_CREDENTIALS=static`) |
| `STORAGE_S3_SECRET_KEY` | `SecretKey` | `string` | — | Secret key (при `STORAGE_S3_CREDENTIALS=static`) |
| `STORAGE_S3_CREDENTIALS` | `Credentials` | `string` | `static` | `static` — ключи из ENV; `chain` — цепочка minio-go (AWS/MinIO env → IAM/IRSA) |
| `STORAGE_S3_BUCKET` | `Bucket` | `string` | — | Имя бакета; часть публичного URL, цель presign и health-пробы |
| `STORAGE_S3_REGION` | `Region` | `string` | — | Регион бакета для SigV4; у MinIO может быть пустым |
| `STORAGE_S3_USE_SSL` | `UseSSL` | `bool` | `false` | TLS до endpoint'а, если тот задан без схемы |
| `STORAGE_S3_BUCKET_LOOKUP` | `BucketLookup` | `string` | `auto` | Адресация бакета: `auto` / `path` / `dns` (virtual-hosted) |
| `STORAGE_S3_CREATE_BUCKET` | `CreateBucket` | `bool` | `false` | Создавать бакет в `OnStart`, если его нет (нужны права `s3:CreateBucket`) |
| `STORAGE_S3_PART_SIZE` | `PartSize` | `uint64` | `16777216` | Размер части multipart в байтах: [5 MiB, 5 GiB]; он же потолок памяти на `Put` |
| `STORAGE_S3_MAX_RETRIES` | `MaxRetries` | `int` | `3` | Повторы minio-go после первой попытки (`0` — без повторов) |
| `STORAGE_S3_PROBE_TIMEOUT` | `ProbeTimeout` | `time.Duration` | `5s` | Дедлайн health-пробы и стартовой проверки (`0` — только дедлайн вызывающего) |
| `STORAGE_S3_PROBE_ON_START` | `ProbeOnStart` | `bool` | `true` | Fail-fast проверка бакета в `OnStart` |
| `STORAGE_S3_URL_STYLE` | `URLStyle` | `string` | `path` | Форма публичного URL: `path` (`<base>/<bucket>/<key>`) или `virtual` (`<base>/<key>`) |
| `STORAGE_PUBLIC_BASE_URL` | `PublicBaseURL` | `string` | — | База публичного URL (CDN или публичный хост бакета) |

`NewConfig()` читает ENV и вызывает `Validate()`; любая ошибка обёрнута в
`ErrConfig` и валит старт приложения. Правила `Validate`:

- `STORAGE_S3_ENDPOINT` — непустой `host[:port]` **или** `http(s)://host[:port]`
  без пути, query и кредов. Схема в значении задаёт TLS сама; `http://…` вместе
  с `STORAGE_S3_USE_SSL=true` — ошибка.
- `STORAGE_S3_BUCKET` — непустой и без `/`.
- `STORAGE_S3_CREDENTIALS` ∈ {`static`, `chain`}; при `static` access и secret
  задаются вместе (оба пустых — анонимный доступ).
- `STORAGE_S3_BUCKET_LOOKUP` ∈ {`auto`, `path`, `dns`}, `STORAGE_S3_URL_STYLE` ∈
  {`path`, `virtual`}.
- `STORAGE_S3_PART_SIZE` ∈ [5 MiB, 5 GiB]; `STORAGE_S3_MAX_RETRIES` ≥ 0;
  `STORAGE_S3_PROBE_TIMEOUT` ≥ 0.
- `STORAGE_PUBLIC_BASE_URL`, если задан, — абсолютный http(s)-URL.

**Публичный URL ≠ endpoint.** `STORAGE_S3_ENDPOINT` — адрес, по которому в S3
ходит сервис (часто внутренний хост кластера). `STORAGE_PUBLIC_BASE_URL` — адрес,
по которому объект читает браузер (CDN или публичный хост бакета); он попадает в
`Ref.URL` и ни в один запрос кита. Если бакет входит в хост базы (CDN,
virtual-hosted), ставьте `STORAGE_S3_URL_STYLE=virtual`, иначе бакет
продублируется в пути.

## Observability

Боевой адаптер `minio` эмитит метрики через
`otel.Meter("git.stit.tech/stit-core/golibinfra/storagekit")` (no-op без
`MeterProvider`):

| Метрика | Тип | Labels | Что |
|---|---|---|---|
| `storage_ops_total` | counter | `op` (`put` / `get` / `stat` / `delete` / `presign_get` / `presign_put`), `result` (`ok` / `error`) | операции по исходу |
| `storage_op_duration_seconds` | histogram (s) | `op` | длительность операции; границы — общие `otelkit.DurationBuckets` (0.005 … 30 s) |

`MemoryStorage` метрик не эмитит. Спанов кит не эмитит; HTTP-вызовы `minio-go`
не инструментированы — трассировка операций storage, если нужна, — на стороне
доменного кода.

Health-проба — `minio.Probe(client, cfg)`: `BucketExists` по `STORAGE_S3_BUCKET`
под именем `storagekit` с дедлайном `STORAGE_S3_PROBE_TIMEOUT`. Регистрируется
потребителем в группу `healthkit.probes` (`healthkit.Provide`).

## API

### `Storage`

```go
type Storage interface {
    Put(ctx context.Context, r io.Reader, contentType string, size int64) (Ref, error)
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
    Stat(ctx context.Context, key string) (ObjectInfo, error)
    Delete(ctx context.Context, key string) error
}
```

`Put` генерирует ключ через `NewKey` и стримит тело; `Get` открывает объект на
чтение (закрывает вызывающий); `Stat` отдаёт метаданные; `Delete` идемпотентен.
Пустой `key` в `Get`/`Stat`/`Delete` — ошибка; отсутствующий объект в `Get`/`Stat`
— `ErrNotFound`.

### `Ref` и `ObjectInfo`

```go
type Ref struct {
    Key         string // `<uuidv7>.<ext>`
    URL         string // публичный стабильный URL, не протухает
    ContentType string // нормализованный: пустой → application/octet-stream
    Size        int64  // реальный размер сохранённого тела
}

type ObjectInfo struct {
    Size         int64
    ContentType  string
    ETag         string
    LastModified time.Time
}
```

### `Presigner`

```go
type Presigner interface {
    PresignGet(ctx context.Context, key string, ttl time.Duration, opts ...PresignOption) (string, error)
    PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
}

func WithResponseContentDisposition(v string) PresignOption
func WithResponseContentType(v string) PresignOption
func ValidatePresignTTL(ttl time.Duration) error // [MinPresignTTL, MaxPresignTTL] = [1 s, 7 d]
```

### `URLBuilder`

```go
func NewURLBuilder(baseURL, bucket, style string) *URLBuilder
func (b *URLBuilder) Build(key string) string
```

`key` — object key, а не URL-путь: сегменты экранируются (`url.PathEscape`),
разделители `/` сохраняются.

### `MemoryStorage` (in-memory)

```go
func NewMemoryStorage(urls *URLBuilder) *MemoryStorage // urls может быть nil
func (m *MemoryStorage) Bytes(key string) ([]byte, bool) // тест-хелпер: копия байтов
func (m *MemoryStorage) Keys() []string                  // тест-хелпер: ключи
```

Потокобезопасен (`sync.RWMutex`), не персистентен; реализует `Storage` и
`Presigner`.

### `minio`-подпакет (боевой адаптер)

```go
func NewClient(cfg storagekit.Config) (*miniogo.Client, error)
func NewStorage(client *miniogo.Client, cfg storagekit.Config, urls *storagekit.URLBuilder, log logkit.Logger) *Storage
func Probe(client *miniogo.Client, cfg storagekit.Config) healthkit.Probe
const ProbeName = "storagekit"
var Module fx.Option // → *miniogo.Client, *Storage, storagekit.Storage, storagekit.Presigner
```

`*minio.Storage` реализует `storagekit.Storage` и `storagekit.Presigner` поверх
`minio-go`. `*miniogo.Client` остаётся в графе — для операций вне порта
(bucket policy, lifecycle, listing).

### Sentinel-ошибки

```go
var ErrConfig        = errors.New("storagekit: invalid config")
var ErrUploadFailed  = errors.New("storagekit: upload failed")
var ErrSizeMismatch  = errors.New("storagekit: body size differs from declared size")
var ErrGetFailed     = errors.New("storagekit: get failed")
var ErrStatFailed    = errors.New("storagekit: stat failed")
var ErrDeleteFailed  = errors.New("storagekit: delete failed")
var ErrPresignFailed = errors.New("storagekit: presign failed")
var ErrNotFound      = errors.New("storagekit: object not found")
```

Проверять через `errors.Is`. Расхождение `size` оборачивает и `ErrUploadFailed`,
и `ErrSizeMismatch`; `Get`/`Stat` отсутствующего объекта — `ErrNotFound` внутри
`ErrGetFailed`/`ErrStatFailed`.

## Тестирование

Потребитель тестирует доменную логику без S3, подменяя `Storage` на
`MemoryStorage`:

```go
mem := storagekit.NewMemoryStorage(storagekit.NewURLBuilder("https://cdn.example.com", "media", storagekit.URLStylePath))
svc := NewService(mem) // Service зависит от storagekit.Storage

ref, err := svc.Upload(ctx, body, "image/png")
data, ok := mem.Bytes(ref.Key) // проверить сохранённые байты
```

Обе реализации проходят один контрактный набор
[`testkit/storagetest`](../testkit/storagetest/) (`storagetest.RunContract`):
контракт `size`, `ErrNotFound`, идемпотентность `Delete`, нормализация
content-type, диапазон TTL presign, уважение ctx. Тест на in-memory поэтому
проверяет то же поведение, что даст прод.

Боевой адаптер тестируется на testcontainers MinIO
(`testkit.StartMinIO`, один контейнер на пакет): контракт, multipart, память на
`Put` с неизвестным размером, fail-fast при расхождении `size`, `Probe`,
стартовые проверки `Module`, round-trip по TLS и отказы S3 (запись и удаление
отбиваются прокси перед контейнером). Под `-short` контейнерные тесты пропускаются;
полный прогон — `make test-full` (нужен Docker).

## Инварианты и подводные камни

- **Память на `Put` ≈ `PartSize`.** Тело целиком не буферизуется, но multipart
  читает part-буфер: `Put` с неизвестной длиной стоит `STORAGE_S3_PART_SIZE`
  памяти (по умолчанию 16 MiB) и ограничивает объект 10000 частями (160 GiB на
  дефолте). Известный `size` ≤ `PartSize` идёт одним PUT без такого буфера:
  граница включающая — ровно `PartSize` байт ещё один запрос, `PartSize + 1`
  уже multipart.
- **`size >= 0` — точная длина, не подсказка.** Обе реализации отклоняют тело
  короче или длиннее заявленного (`ErrSizeMismatch`), объект не остаётся: у
  seekable-источника расхождение ловится до запроса, у потока — одной попыткой
  PUT и удалением загруженного. Поток обязан вернуть EOF после `size` байт,
  иначе проверка хвоста ждёт его чтения (отмена ctx её не ускоряет — она лишь
  ограничивает ожидание). Источник, который объявляет `io.Seeker`, но не даёт
  вернуть позицию, отвергается (`ErrUploadFailed`): читать его дальше значило бы
  грузить не то тело.
- **Бакет должен существовать.** Кит его не создаёт, если не задан
  `STORAGE_S3_CREATE_BUCKET=true`: приложению обычно не нужны права
  `s3:CreateBucket`, а опечатка в имени должна падать, а не заводить новый бакет.
- **Ключи — uuidv7, не content-addressed.** Каждый `Put` даёт новый уникальный
  ключ независимо от содержимого; дедупликации по хэшу нет. uuidv7 — k-sortable
  по времени создания.
- **`URL` стабилен и публичен.** `Ref.URL` не протухает — read-side читает
  напрямую. Доступ к бакету должен это допускать (публичное чтение либо CDN
  перед ним); если чтение закрыто, выдавайте `PresignGet`.
- **`Delete` идемпотентен.** Повторное удаление и удаление несуществующего
  ключа — `nil`: S3 не сообщает, был ли объект. Отсутствие объекта различают
  `Get`/`Stat` (`ErrNotFound`).
- **Пустой content-type нормализуется.** `Put` с `""` сохраняет объект как
  `application/octet-stream` и возвращает этот же тип в `Ref` — `Ref` не
  расходится с метаданными объекта.
- **Ретраи и таймауты.** minio-go повторяет запрос `STORAGE_S3_MAX_RETRIES` раз
  (по умолчанию 3, backoff 200 ms → 1 s); транспорт по умолчанию — dial 30 s,
  response-header 60 s. Детерминированные ошибки клиента (расхождение `size`)
  не ретраятся.
- **`Config` регистрирует потребитель.** Ни `storagekit.Module`, ни
  `minio.Module` не провайдят `Config` — добавьте `fx.Provide(storagekit.NewConfig)`
  (или свой источник `Config`) сами.
- **Per-concern endpoint.** `STORAGE_S3_ENDPOINT` — отдельный «шов» топологии:
  storage можно развести на отдельный хост, не трогая код (см. `../README.md
  § Per-concern endpoints`).
