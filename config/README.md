# config

Единый загрузчик конфигурации для китов golibinfra: ENV + Vault через
композицию источников. Источник правды по поведению — код пакета;
сводная строка в общей карте — [`../README.md` § Пакеты](../README.md#пакеты).

## Назначение

Каждый кит держит свою `Config`-структуру с `envconfig`-тегами и (опционально)
`vault`-тегами на секретных полях. `config` не знает про эти структуры — он даёт
композицию **источников** (`Source`), которые умеют заполнять *любую* переданную
структуру:

- `Env(prefix…)` — читает значения из переменных окружения по `envconfig`-тегам;
- `Vault(client, path)` — читает KV v2 секрет из Vault и раскладывает его по
  полям с тегом `vault:"<key>"`.

`Load(ctx, dst, sources…)` прогоняет `dst` через каждый источник по порядку.
Поздние источники перезатирают поля более ранних — это и есть контракт
приоритета: **env-default'ы → vault-override**. Так секрет (JWT-ключ, пароль БД)
живёт в Vault, а остальная конфигурация — в ENV, и обе части собираются в одну
структуру без специального кода в ките.

`Module` поднимает Vault-клиент из ENV (`VAULT_ADDR`/`VAULT_TOKEN`/`VAULT_NAMESPACE`
+ `VAULT_PATH`) и провайдит готовый `[]Source` в fx-граф, чтобы приложение и
киты получали значения единообразно.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/config"
```

Версию модуля не дублируем здесь — она в [`../README.md` § Использование в
проекте](../README.md#использование-в-проекте).

## Быстрый старт

### Через fx

`config.Module` провайдит в граф `[]config.Source` (а также `VaultClientConfig`,
`VaultPathConfig` и `*vault.Client`). Кит зависит от `[]config.Source` и грузит
свою структуру через `config.Load`:

```go
package authnkit

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/config"
	"go.uber.org/fx"
)

type Config struct {
	Issuer    string `envconfig:"JWT_ISSUER" default:"stit"`
	JWTSecret string `vault:"jwt_secret"` // придёт из Vault, не из ENV
}

func newConfig(srcs []config.Source) (Config, error) {
	var c Config
	if err := config.Load(context.Background(), &c, srcs...); err != nil {
		return c, err
	}
	return c, nil
}

var Module = fx.Module("authnkit",
	fx.Provide(newConfig),
	// …
)
```

Приложение собирает граф из обоих модулей:

```go
fx.New(
	config.Module,   // env + (опц.) vault → []config.Source
	authnkit.Module, // потребляет []config.Source
)
```

### Без fx

Источники можно собрать руками — `Module` ничего магического не делает:

```go
client, err := config.NewVaultClient(config.VaultClientConfig{
	Addr:  os.Getenv("VAULT_ADDR"),
	Token: os.Getenv("VAULT_TOKEN"),
})
if err != nil {
	return err
}

srcs := []config.Source{config.Env()}
if client != nil {
	srcs = append(srcs, config.Vault(client, "secret/data/myapp"))
}

var cfg MyConfig
if err := config.Load(ctx, &cfg, srcs...); err != nil {
	return err
}
```

Точные сигнатуры — см. [§ API](#api).

## Конфигурация

`config.VaultClientConfig` — **part of public API** (см. [`../README.md` §
Стабильность API](../README.md#стабильность-api)): менять имена/типы полей или их
`envconfig`-теги = breaking change, идёт мажорным апгрейдом.

`Module` читает Vault-клиент из этих переменных, плюс путь к секрету (`VAULT_PATH`).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `VAULT_ADDR` | `VaultClientConfig.Addr` | `string` | `""` | Адрес Vault как secret-source. Пусто → клиент не создаётся (`NewVaultClient` возвращает `nil, nil`), Vault-источник в `[]Source` не добавляется. |
| `VAULT_TOKEN` | `VaultClientConfig.Token` | `string` | `""` | Токен аутентификации в Vault. Непустой → ставится через `SetToken`. Пусто → токен берётся по умолчаниям клиента Vault (env `VAULT_TOKEN` / `~/.vault-token`). |
| `VAULT_NAMESPACE` | `VaultClientConfig.Namespace` | `string` | `""` | Vault namespace (Enterprise/HCP). Непустой → ставится через `SetNamespace`. |
| `VAULT_PATH` | `VaultPathConfig.Path` | `string` | `""` | KV v2 путь к секрету (например `secret/data/myapp`). Пусто → Vault-источник не добавляется в `[]Source`, даже если клиент создан. |

`VAULT_PATH` живёт в отдельной `VaultPathConfig` (не в публичном
`VaultClientConfig`), потому что относится к выбору источника, а не к самому
клиенту.

См. также [`../README.md` § Per-concern endpoints](../README.md#per-concern-endpoints-multi-host):
`VAULT_ADDR` — это «шов» кита `config`.

## Observability

Пакет `config` метрик и спанов **не эмитит**. Это чистый загрузчик: вызывается на
старте при сборке fx-графа, на горячем пути не работает. В [`../README.md` § Что
эмитит каждый пакет](../README.md#что-эмитит-каждый-пакет-короткий-reference)
строки для `config` нет — по той же причине.

## API

### `Source`

```go
type Source interface {
	Apply(ctx context.Context, dst any) error
}
```

Источник, который заполняет переданную структуру `dst` (всегда указатель на
struct). Реализации: `Env` и `Vault`. Кит работает не с конкретной реализацией, а
со срезом `[]Source` — это и есть точка композиции.

### `Load`

```go
func Load(ctx context.Context, dst any, sources ...Source) error
```

Прогоняет `dst` через каждый `Source` по порядку. Поздний источник перезатирает
поля раннего (env → vault-override). Ошибка любого источника оборачивается с его
индексом (`config: source[i]: …`) и прерывает загрузку. Это основной вызов внутри
конструктора `Config` каждого кита.

### `Env`

```go
func Env(prefix ...string) Source
```

Источник из переменных окружения через `envconfig`. Необязательный `prefix`
добавляет приставку к именам переменных (передаётся в `envconfig.Process`); без
аргумента — без префикса. В большинстве китов используется как `config.Env()`.

### `Vault` / `NewVaultClient` / `VaultClientConfig`

```go
func NewVaultClient(cfg VaultClientConfig) (*vault.Client, error)
func Vault(client *vault.Client, path string) Source

type VaultClientConfig struct {
	Addr      string `envconfig:"VAULT_ADDR"`
	Token     string `envconfig:"VAULT_TOKEN"`
	Namespace string `envconfig:"VAULT_NAMESPACE"`
}
```

- `NewVaultClient` строит `*vault.Client` из `VaultClientConfig`. Если `Addr`
  пуст — возвращает `nil, nil` (Vault выключен, не ошибка). При непустом `Token`
  / `Namespace` вызывает `SetToken` / `SetNamespace`.
- `Vault(client, path)` — источник, читающий KV v2 секрет по `path`. Путь
  делится на mount и ключ по первому `/` (`secret/data/foo` → mount `secret`,
  ключ `data/foo`), читается через `KVv2(mount).Get(ctx, key)`. Затем значения
  раскладываются по полям `dst` с тегом `vault:"<key>"`.
  - Если `client == nil` — `Apply` ничего не делает (no-op), удобно для условной
    проводки.
  - Пустой/отсутствующий секрет по пути → ошибка `config: vault: empty secret`.
  - Маппится только в **string-поля**; нестроковое значение в Vault или
    несовместимое поле → ошибка.

Используй `VaultClientConfig` + `NewVaultClient`, когда строишь Vault-клиент сам
(без `Module`); `Vault(client, path)` — когда добавляешь Vault как источник в
свой `[]Source`. В fx-сценарии всё это делает `Module`.

### `Module`

```go
var Module = fx.Module("config", …)
```

Провайдит в fx-граф:

- `VaultClientConfig` — из ENV;
- `VaultPathConfig` (`VAULT_PATH`) — из ENV;
- `*vault.Client` — через `NewVaultClient` (может быть `nil`);
- `[]Source` — `[]Source{Env()}`, плюс `Vault(client, path)`, если клиент не
  `nil` **и** `VAULT_PATH` непуст.

Кит подключает `config.Module` и зависит от `[]config.Source`.

## Тестирование

- **Маппинг Vault → структура** проверяется без живого Vault через внутренний
  `applyVaultData(data, dst)`: подаёшь `map[string]any` и проверяешь, что поля с
  тегом `vault:"…"` заполнились (см. `vault_test.go`). Сюда же — негативные
  кейсы: не-указатель, не-struct, нестроковое значение.
- **Разбор пути** покрыт юнит-тестами `splitMount` / `splitKey`.
- **Fuzz** на `applyVaultData` (`vault_fuzz_test.go`) гоняет произвольные
  строковые значения (включая `\x00`, длинные строки, юникод) — `Apply` не
  должен паниковать.
- **ENV-источник** тестируется выставлением переменных окружения (`t.Setenv`) и
  вызовом `config.Load(ctx, &cfg, config.Env())` с проверкой заполненных полей.
- Тесты, требующие живого Vault, исключай из `-short`-прогона (CI гоняет
  `go test -short -race ./...`).

Запуск:

```bash
go test -short -race ./config/...
go test -run Fuzz -fuzz FuzzApplyVaultData ./config/   # отдельный fuzz-прогон
```

## Инварианты и подводные камни

- **Приоритет = порядок в `Load`.** Источники применяются слева направо, поздний
  перезатирает раннего. `Module` ставит `Env()` первым, `Vault(…)` — вторым:
  значения из Vault имеют приоритет над ENV для тех же полей.
- **ENV и Vault мапятся разными тегами.** ENV-поля — по `envconfig:"…"`,
  Vault-поля — по `vault:"…"`. Поле без `vault`-тега Vault-источник игнорирует;
  поле без `envconfig`-тега не возьмётся из ENV. Секретное поле обычно несёт
  только `vault:"…"`, чтобы секрет не подхватывался из окружения.
- **Секреты — в Vault, не в ENV.** Кит кладёт `vault:"…"` на чувствительные поля
  (ключи, пароли, токены), а несекретную конфигурацию — на `envconfig:"…"`. Так
  секреты не попадают в окружение процесса.
- **Vault опционален.** Без `VAULT_ADDR` клиент = `nil`, `[]Source` содержит
  только `Env()` — приложение поднимается на одном ENV. Vault-источник
  добавляется только когда есть и клиент (`VAULT_ADDR`), и `VAULT_PATH`.
- **Vault мапит только string-поля.** Нестроковое значение в секрете или
  несовместимое поле приводит к ошибке `Apply`. Числа/булевы держи в ENV.
- **Аутентификация — токеном.** Пакет ставит токен из `VAULT_TOKEN` (если задан)
  и namespace из `VAULT_NAMESPACE`. Иные auth-методы (AppRole и т.п.) пакет
  сам не выполняет — либо передавай уже выданный токен через `VAULT_TOKEN`, либо
  строй и аутентифицируй `*vault.Client` снаружи и подключай его через
  `Vault(client, path)`.
- **KV v2 путь.** Секрет читается как KV **v2** (`KVv2`). Путь должен включать
  сегмент `data` так, как ожидает движок (например `secret/data/myapp`); первый
  `/` делит строку на mount и ключ.
- **`VaultClientConfig` — public API.** Изменение полей/тегов — breaking,
  только мажором.
