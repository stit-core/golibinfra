# amocrmkit — конфигурация

## Config

`amocrmkit.Config` несёт только то, что одинаково для всех аккаунтов.
Per-account-секреты (client_id/client_secret, токены, channel-secret) сюда **не
входят** — они приходят аргументами вызовов и живут в token-store потребителя.

`NewConfig() (Config, error)` читает значения из окружения через
`envconfig.Process("", &c)` и вызывает `Validate`. `Module` сам `Config` не
provide'ит — caller регистрирует `fx.Provide(amocrmkit.NewConfig)` (та же
asymmetry, что в `pgkit`).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `AMOCRM_RATE_RPS` | `RateRPS` | `int` | `7` | потолок rps на аккаунт; общий бюджет REST + amoJo + Drive + OAuth; `<= 0` → `ErrConfig` |
| `AMOCRM_HOST_OVERRIDE` | `HostOverride` | `string` | `""` | если задан — все контуры идут на этот `scheme://host` вместо `https://<subdomain>.amocrm.<tld>` (путь/query/заголовки/подпись сохраняются). Для sandbox/proxy/e2e против fake-amo. В проде пусто. Не `http(s)://host` → `ErrConfig` |

## httpclient.Config

Таймаут, retry, backoff и circuit-breaker задаёт `httpclient.Config`
(`HTTPCLIENT_TIMEOUT`, `HTTPCLIENT_MAX_RETRIES`, `HTTPCLIENT_RETRY_WAIT`,
`HTTPCLIENT_RETRY_MAX_WAIT`, `HTTPCLIENT_RETRY_NON_IDEMPOTENT`, breaker) — см.
[httpclient](../../httpclient/README.md). Кит собирает из неё **свой**
`*httpclient.Client` с хуком лимитера на каждую попытку; во fx она берётся из
графа (`fx.Provide(httpclient.NewConfig)`), то есть ENV общий с остальными
исходящими вызовами приложения.

## Public API

Имена полей и envconfig-теги `Config` — **part of public API**. Их изменение
(переименование поля, смена тега или типа) — breaking change и идёт мажорным
апгрейдом. См. корневой [README](../../README.md) § Стабильность API.

## Тюнинг

- **`RateRPS`** — значение должно соответствовать лимиту amoCRM для аккаунта (по
  умолчанию 7 rps). Понижай, если несколько процессов делят бюджет одного
  аккаунта вне общего лимитера; не повышай выше потолка amoCRM — лишнее упрётся в
  429 (см. [observability.md](observability.md), метрика
  `amocrm_rate_limited_total`).
- **Ретраи** — `HTTPCLIENT_MAX_RETRIES` работает и здесь: повторные попытки
  ждут слот лимитера аккаунта, поэтому не пробивают бюджет, но удлиняют вызов.
- **`HostOverride`** — оставляй пустым в проде. Заполнение редиректит **весь**
  трафик клиента на один хост, включая amoJo и OAuth.

## Конструктор

```go
func New(cfg Config, hcCfg httpclient.Config, clock clockkit.Clock, log logkit.Logger, opts ...Option) (*Client, error)
```

- `cfg` проходит `Validate`; `hcCfg` — `httpclient.Config.Validate` внутри
  `httpclient.New`.
- `clock == nil` → подставляется `clockkit.Real{}`; `log == nil` → `logkit.Nop()`.
- Опции:

| Опция | Назначение |
|---|---|
| `WithTransport(http.RoundTripper)` | базовый транспорт собираемого китом `httpclient` (тесты, proxy); поверх него httpclient ставит otelhttp |
| `WithHTTPClient(*httpclient.Client)` | готовый httpclient вместо собираемого (`hcCfg` игнорируется). Его внутренние ретраи не проходят через лимитер аккаунта — только первая попытка; для боевой проводки используйте `hcCfg` |

Во fx `Module` зовёт `New` без опций (вариадик fx не заполняет).
