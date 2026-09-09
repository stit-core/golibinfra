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
| `AMOCRM_MAX_WAIT` | `MaxWait` | `time.Duration` | `5s` | потолок ожидания слота лимитера аккаунта (очередь + пауза после 429). Дольше — вызов отклоняется локально с `ErrRateLimited`, не занимая горутину. `< 0` → `ErrConfig`; `0` → дефолт |
| `AMOCRM_HOST_OVERRIDE` | `HostOverride` | `string` | `""` | если задан — все контуры идут на этот `scheme://host` вместо `https://<subdomain>.amocrm.<tld>` (путь/query/заголовки/подпись сохраняются) и отключается allowlist доменов Drive. Для sandbox/proxy/e2e против fake-amo. В проде пусто. Не `http(s)://host` (в т.ч. с путём, query или userinfo) → `ErrConfig` |

`(*Config).Validate()` подставляет дефолт вместо нулевого `MaxWait`, поэтому
собранный вручную `Config{RateRPS: 7}` безопасен.

## httpclient.Config

Таймаут, retry, backoff, редиректы и circuit-breaker задаёт `httpclient.Config`
(`HTTPCLIENT_TIMEOUT`, `HTTPCLIENT_MAX_RETRIES`, `HTTPCLIENT_RETRY_WAIT`,
`HTTPCLIENT_RETRY_MAX_WAIT`, `HTTPCLIENT_RETRY_NON_IDEMPOTENT`,
`HTTPCLIENT_MAX_REDIRECTS`, `HTTPCLIENT_ALLOW_CROSS_HOST_REDIRECTS`,
`HTTPCLIENT_SSE_MAX_EVENT_BYTES`, `HTTPCLIENT_BREAKER_*`) — см.
[httpclient](../../httpclient/README.md). Кит собирает из неё **свой**
`*httpclient.Client` с хуком лимитера на каждую попытку.

- `New` валидирует переданную конфигурацию (`ErrConfig`) — клиента без таймаута
  собрать нельзя: нули `httpclient.Validate` заменяет дефолтами (`Timeout` 10s).
- **`RetryNonIdempotent` кит принудительно выключает у своего клиента**: повтор
  POST-создания в amoCRM даёт дубль сущности, серверной идемпотентности нет
  (`request_id` дедуплицирует только внутри одного батча). Повтор разрешён явно
  и точечно — сейчас у `amojo.typing` и `amojo.delivery_status`.
- Во fx конфигурация берётся из графа (`fx.Provide(httpclient.NewConfig)`), то
  есть ENV общий с остальными исходящими вызовами приложения. Отдельная политика
  для amoCRM — `fx.Provide(func() (httpclient.Config, error) { return
  httpclient.NewConfigPrefixed("AMOCRM") })`: переменные `AMOCRM_HTTPCLIENT_*` с
  fallback на голые `HTTPCLIENT_*`.

## Public API

Имена полей и envconfig-теги `Config` — **part of public API**. Их изменение
(переименование поля, смена тега или типа) — breaking change: выпускается
minor-релизом, помечается `!` в Conventional Commit и строкой миграции в
аннотации тега. См. корневой [README](../../README.md) § Версионирование.

## Тюнинг

- **`RateRPS`** — значение должно соответствовать лимиту amoCRM для аккаунта (по
  умолчанию 7 rps). Понижай, если несколько процессов делят бюджет одного
  аккаунта вне общего лимитера; не повышай выше потолка amoCRM — лишнее упрётся в
  429 (см. [observability.md](observability.md), метрика
  `amocrm_rate_limited_total`).
- **`MaxWait`** — повышай, если предпочитаешь ждать слот, а не получать
  `ErrRateLimited` на всплеске; понижай, чтобы всплеск быстрее отдавал управление
  очереди потребителя. Значение больше типичного `retry_after` amoCRM (30–300 с)
  превращает паузу после 429 в блокировку горутины.
- **Ретраи** — `HTTPCLIENT_MAX_RETRIES` работает и здесь: повторные попытки
  ждут слот лимитера аккаунта, поэтому не пробивают бюджет, но удлиняют вызов.
  После 429 лимитер аккаунта штрафуется на `retry_after`, поэтому ретраи и
  параллельные вызовы того же аккаунта отступают вместе.
- **`HostOverride`** — оставляй пустым в проде. Заполнение редиректит **весь**
  трафик клиента на один хост, включая amoJo и OAuth.

## Конструктор

```go
func New(cfg Config, hcCfg httpclient.Config, clock clockkit.Clock, log logkit.Logger, opts ...Option) (*Client, error)
```

- `cfg` и `hcCfg` проходят `Validate` (обе ошибки — `ErrConfig`).
- `clock == nil` → подставляется `clockkit.Real{}`; `log == nil` → `logkit.Nop()`.
- Опции:

| Опция | Назначение |
|---|---|
| `WithTransport(http.RoundTripper)` | базовый транспорт собираемого китом `httpclient` (тесты, proxy); поверх него httpclient ставит otelhttp |
| `WithHTTPClient(*httpclient.Client)` | готовый httpclient вместо собираемого (`hcCfg` только валидируется). Его ретраи не проходят через лимитер аккаунта и не поднимают `retry_after` из тела 429 — для боевой проводки используйте `hcCfg` |

Во fx `Module` зовёт `New` без опций (вариадик fx не заполняет).
