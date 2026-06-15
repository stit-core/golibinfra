# amocrmkit — конфигурация

## Config

`amocrmkit.Config` несёт только то, что одинаково для всех аккаунтов.
Per-account-секреты (client_id/client_secret, токены, channel-secret) сюда **не
входят** — они приходят аргументами вызовов и живут в token-store gateway.

`NewConfig() (Config, error)` читает значения из окружения через
`envconfig.Process("", &c)`. `Module` сам `Config` не provide'ит — caller
регистрирует `fx.Provide(amocrmkit.NewConfig)` (та же asymmetry, что в `pgkit`).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `AMOCRM_TIMEOUT` | `Timeout` | `time.Duration` | `10s` | таймаут per-call (значение конфига; фактический таймаут HTTP задаёт `httpclient`) |
| `AMOCRM_RATE_RPS` | `RateRPS` | `int` | `7` | потолок rps на аккаунт; общий бюджет REST + amoJo + Drive |
| `AMOCRM_HOST_OVERRIDE` | `HostOverride` | `string` | `""` | если задан — все контуры идут на этот `scheme://host` вместо `https://<subdomain>.amocrm.<tld>` (путь/заголовки/подпись сохраняются). Для sandbox/proxy/e2e против fake-amo. В проде пусто |

## Public API

Имена полей и envconfig-теги `Config` — **part of public API**. Их изменение
(переименование поля, смена тега или типа) — breaking change и идёт мажорным
апгрейдом. См. корневой [README](../../README.md) § Стабильность API.

## Тюнинг

- **`RateRPS`** — `New` требует `RateRPS > 0` (иначе ошибка конструктора). Значение
  должно соответствовать лимиту amoCRM для аккаунта (по умолчанию 7 rps). Понижай,
  если несколько процессов делят бюджет одного аккаунта вне общего лимитера; не
  повышай выше потолка amoCRM — лишнее упрётся в 429 (см.
  [observability.md](observability.md), метрика `amocrm_rate_limited_total`).
- **`Timeout`** — конфиг-значение per-call; реальные сетевые таймауты, retry и
  circuit-breaker конфигурируются на стороне `httpclient`, который передаётся в
  `New`.
- **`HostOverride`** — оставляй пустым в проде. Заполнение редиректит **весь**
  трафик клиента на один хост, включая amoJo и OAuth.

## Зависимости конструктора

`New(cfg Config, hc *httpclient.Client, clock clockkit.Clock, log logkit.Logger)`:

- `hc` — обязателен (`nil` → ошибка).
- `clock == nil` → подставляется `clockkit.Real{}`.
- `log == nil` → подставляется `logkit.Nop()`.
