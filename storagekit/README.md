# storagekit

S3-совместимое объектное хранилище: порт `Storage` (`Put`/`Delete`), построитель
стабильного публичного URL (`URLBuilder`), генератор ключей `NewKey` и адаптеры —
боевой (`minio`-подпакет поверх MinIO/S3) и in-memory (для тестов потребителя).

Kit доменно-нейтрален: стримит байты как есть, без буферизации тела в памяти.
Image/file-семантика — валидация mime, ограничение размера, декод картинки — на
стороне приложения в доменных value-объектах, не здесь.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/storagekit"
import "git.stit.tech/stit-core/golibinfra/storagekit/minio" // боевой адаптер
```

Версия модуля и настройка доступа к приватной Gitea — в корневом
[`../README.md`](../README.md).

## Быстрый старт

### fx-проводка

`storagekit.Module` даёт `*URLBuilder` из `Config`. Боевой `Storage` (S3-адаптер)
регистрирует подпакет `storagekit/minio` своим `Module`. `Config` потребитель
регистрирует сам (та же асимметрия, что у `cachekit`/`pgkit`/`natskit`).

```go
import (
    "go.uber.org/fx"

    "git.stit.tech/stit-core/golibinfra/storagekit"
    skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
)

fx.New(
    fx.Provide(storagekit.NewConfig), // Config из ENV (STORAGE_S3_*)
    storagekit.Module,                // → *storagekit.URLBuilder
    skminio.Module,                   // → storagekit.Storage (MinIO/S3)
    // ... logkit.Module и пр.
)
```

После этого в граф можно инжектить `storagekit.Storage` и `*storagekit.URLBuilder`.

### Put / Delete

```go
func handle(ctx context.Context, st storagekit.Storage, body io.Reader, size int64) error {
    ref, err := st.Put(ctx, body, "image/png", size)
    if err != nil {
        return err // errors.Is(err, storagekit.ErrUploadFailed) при отказе/расхождении size
    }
    // ref.Key — `<uuidv7>.png`; ref.URL — публичный стабильный URL; ref.Size — реальный размер
    _ = ref

    return st.Delete(ctx, ref.Key) // идемпотентен: отсутствующий ключ — не ошибка
}
```

Сигнатуры:

```go
Put(ctx context.Context, r io.Reader, contentType string, size int64) (Ref, error)
Delete(ctx context.Context, key string) error
```

`size >= 0` — точная длина тела: тело короче или длиннее → `ErrUploadFailed`,
объект не остаётся. `size < 0` — длина неизвестна: тело стримится до EOF
(multipart), `Ref.Size` — фактический размер.

### URLBuilder и NewKey

```go
urls := storagekit.NewURLBuilder("https://cdn.example.com", "media")
url := urls.Build("abc.png")        // https://cdn.example.com/media/abc.png

key, err := storagekit.NewKey("image/png") // "<uuidv7>.png"
```

`Build` собирает `<base>/<bucket>/<key>`, схлопывая хвостовые `/` у base. `NewKey`
выводит расширение из `contentType` (`image/jpeg`→`.jpg`, `image/png`→`.png`,
`image/webp`→`.webp`, `application/pdf`→`.pdf`; неизвестный тип → без расширения).

### in-memory адаптер (тесты)

```go
mem := storagekit.NewMemoryStorage(storagekit.NewURLBuilder("https://cdn.example.com", "media"))
ref, _ := mem.Put(ctx, bytes.NewReader(data), "image/png", int64(len(data)))
got, ok := mem.Get(ref.Key) // тест-хелпер: вернуть сохранённые байты
```

`MemoryStorage` реализует `Storage`; ключи и URL совпадают по схеме с боевым
адаптером, поэтому тест на нём проверяет ту же контрактную форму `Ref`.

## Конфигурация

`storagekit.Config` — **part of public API** (см. `../README.md § Стабильность
API`). Менять имена/типы полей или их `envconfig`-теги = breaking change, идёт
мажорным апгрейдом.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `STORAGE_S3_ENDPOINT` | `Endpoint` | `string` | — | Хост S3 / MinIO (per-concern endpoint — разное значение = разный хост) |
| `STORAGE_S3_ACCESS_KEY` | `AccessKey` | `string` | — | Access key (S3 v4 static creds) |
| `STORAGE_S3_SECRET_KEY` | `SecretKey` | `string` | — | Secret key (S3 v4 static creds) |
| `STORAGE_S3_BUCKET` | `Bucket` | `string` | — | Имя бакета; часть публичного URL и health-probe |
| `STORAGE_S3_USE_SSL` | `UseSSL` | `bool` | `false` | TLS до endpoint'а |
| `STORAGE_S3_REGION` | `Region` | `string` | — | Регион bucket'а для S3 v4 signing; у MinIO может быть пустым |
| `STORAGE_PUBLIC_BASE_URL` | `PublicBaseURL` | `string` | — | База публичного URL (`<base>/<bucket>/<key>`); read-side читает напрямую |

`NewConfig()` заполняет `Config` из ENV через `envconfig` и возвращает ошибку
парсинга. В `Module` он **не** включён — `Config` регистрирует потребитель сам.

`storagekit/minio.NewClient` требует непустые `Endpoint` и `Bucket`, иначе вернёт
ошибку конструирования.

## Observability

Боевой адаптер `minio` эмитит метрики через
`otel.Meter("git.stit.tech/stit-core/golibinfra/storagekit")` (no-op без
`MeterProvider`):

| Метрика | Тип | Labels | Что |
|---|---|---|---|
| `storage_ops_total` | counter | `op` (`put` / `delete` / `presign`), `result` (`ok` / `error`) | операции по исходу |
| `storage_op_duration_seconds` | histogram (s) | `op` | длительность операции |

`MemoryStorage` метрик не эмитит. Спанов кит не эмитит; HTTP-вызовы `minio-go`
не инструментированы — трассировка операций storage, если нужна, — на стороне
доменного кода.

## API

### `Storage`

```go
type Storage interface {
    Put(ctx context.Context, r io.Reader, contentType string, size int64) (Ref, error)
    Delete(ctx context.Context, key string) error
}
```

`Put` генерирует ключ через `NewKey`, стримит тело и возвращает `Ref`; `Delete`
удаляет по ключу и идемпотентен (S3 отвечает 204 и на отсутствующий ключ).

### `Ref`

```go
type Ref struct {
    Key         string // `<uuidv7>.<ext>`
    URL         string // публичный стабильный URL, не протухает
    ContentType string
    Size        int64  // реальный размер сохранённого тела
}
```

Стабильная ссылка на сохранённый объект: read-side читает по `URL` напрямую.

### `NewKey(contentType string) (string, error)`

Ключ `<uuidv7>.<ext>`. uuidv7 даёт лексикографически возрастающие, k-sortable по
времени создания ключи; расширение выводится из `contentType`. Ошибка — только при
сбое генерации uuid (обёрнута в `ErrUploadFailed`).

### `URLBuilder`

```go
func NewURLBuilder(baseURL, bucket string) *URLBuilder
func (b *URLBuilder) Build(key string) string // <base>/<bucket>/<key>
```

Хвостовые `/` у `baseURL` схлопываются.

### `MemoryStorage` (in-memory)

```go
func NewMemoryStorage(urls *URLBuilder) *MemoryStorage
func (m *MemoryStorage) Put(ctx, r, contentType, size) (Ref, error) // реализует Storage
func (m *MemoryStorage) Delete(ctx, key) error                      // реализует Storage; идемпотентен
func (m *MemoryStorage) PresignedGet(ctx, bucket, key, ttl) (string, error) // реализует Presigner (детерминированная псевдо-подпись)
func (m *MemoryStorage) Get(key string) ([]byte, bool)              // тест-хелпер
```

Потокобезопасен (`sync.RWMutex`), не персистентен; контракт `size` совпадает с
боевым адаптером.

### `minio`-подпакет (боевой адаптер)

```go
func NewClient(cfg storagekit.Config) (*miniogo.Client, error)
func NewUploader(client *miniogo.Client, cfg storagekit.Config, urls *storagekit.URLBuilder) *Uploader
func Probe(client *miniogo.Client, cfg storagekit.Config) healthkit.Probe
var Module fx.Options // → *miniogo.Client, *Uploader, storagekit.Storage, storagekit.Presigner
```

`*Uploader` реализует `storagekit.Storage` и `storagekit.Presigner` поверх
`minio-go`. `Probe` возвращает health-probe (`BucketExists` по `cfg.Bucket`) —
регистрируется потребителем в группу `health` (`healthkit.Provide`). `Put`
передаёт reader в `PutObject` как есть; при известном `size` клиент читает
ровно `size` байт, хвост длиннее заявленного ловится пробным чтением после
загрузки — объект в этом случае удаляется, результат `ErrUploadFailed`.

### Sentinel-ошибки

```go
var ErrUploadFailed  = errors.New("storagekit: upload failed")  // отказ загрузки, расхождение size, сбой uuid
var ErrDeleteFailed  = errors.New("storagekit: delete failed")  // отказ S3 на RemoveObject
var ErrPresignFailed = errors.New("storagekit: presign failed") // пустые bucket/key или отказ подписи
```

Проверять через `errors.Is`.

## Тестирование

Потребитель тестирует доменную логику без S3, подменяя `Storage` на
`MemoryStorage`:

```go
mem := storagekit.NewMemoryStorage(storagekit.NewURLBuilder("https://cdn.example.com", "media"))
svc := NewService(mem) // Service зависит от storagekit.Storage

ref, err := svc.Upload(ctx, body, "image/png")
// ...assert по ref.URL / ref.Key
data, ok := mem.Get(ref.Key) // проверить сохранённые байты
```

`Get` — единственный метод сверх интерфейса `Storage`, для assert'ов в тестах.

Боевой адаптер покрыт тестами на testcontainers MinIO
(`minio/uploader_minio_test.go`): `Put`/`Stat`/`Delete`, стриминг с неизвестным
размером, отказ при расхождении `size` (бакет остаётся пустым), метрики. Под
`-short` они пропускаются; полный прогон — `make test-full` (нужен Docker).

## Инварианты и подводные камни

- **Ключи — uuidv7, не content-addressed.** `NewKey` строит `<uuidv7>.<ext>`:
  каждый `Put` даёт новый уникальный ключ независимо от содержимого. Дедупликации
  по хэшу содержимого нет. Плюс uuidv7 — k-sortable по времени создания.
- **`URL` стабилен и публичен.** `Ref.URL` (`<PublicBaseURL>/<bucket>/<key>`) не
  протухает — read-side читает напрямую, без подписанных/временных ссылок. Доступ к
  бакету должен это допускать (публичное чтение либо CDN перед ним).
- **`size >= 0` — точная длина, не подсказка.** И боевой, и in-memory адаптер
  отклоняют тело короче или длиннее заявленного (`ErrUploadFailed`, объект не
  остаётся). Неизвестна длина — передавайте `-1`: тело стримится до EOF.
- **`Delete` идемпотентен.** Повторное удаление и удаление несуществующего
  ключа — `nil`; «объект не найден» кит не различает (S3 не сообщает об этом).
- **`minio` vs in-memory.** `MemoryStorage` — только для тестов и локального dev:
  не персистентен, теряет данные с процессом. В проде — `storagekit/minio.Module`.
  Оба согласованы по схеме `Ref` (ключ/URL), так что тест на in-memory проверяет ту
  же форму, что вернёт прод.
- **`Config` регистрирует потребитель.** Ни `storagekit.Module`, ни `minio.Module`
  не провайдят `Config` — добавьте `fx.Provide(storagekit.NewConfig)` (или свой
  источник `Config`) сами.
- **Per-concern endpoint.** `STORAGE_S3_ENDPOINT` — отдельный «шов» топологии:
  storage можно развести на отдельный хост, не трогая код (см. `../README.md
  § Per-concern endpoints`).
