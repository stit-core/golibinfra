# authnkit — observability

Единый источник правды по сигналам кита. Имена/типы/labels берутся из
`metrics.go` и точек записи в `jwt.go` / `hasher.go` / `totp.go`.

Meter: `git.stit.tech/stit-core/golibinfra/authnkit` (через `internal/otelkit`).
Спанов кит не эмитит; `ctx` в сигнатурах нужен, чтобы измерения несли baggage и
exemplar'ы вызывающего.

## Метрики

| Метрика | Тип | Unit | Labels | Назначение |
|---|---|---|---|---|
| `authn_jwt_verifications_total` | Int64 Counter | — | `result` | Исход разбора JWT в `HS256JWT.Parse`/`ParseAccess`/`ParseRefresh`. |
| `authn_password_hash_duration_seconds` | Float64 Histogram | `s` | — | Время одного `Argon2idHasher.Hash`. |
| `authn_password_verify_duration_seconds` | Float64 Histogram | `s` | — | Время одного `Argon2idHasher.Verify` или `VerifyDummy`. |
| `authn_totp_verifications_total` | Int64 Counter | — | `result` | Исход `TOTPGenerator.Verify`/`VerifyOnce`. |

Границы бакетов обеих гистограмм — общие для репозитория `otelkit.DurationBuckets()`
(`0.005 … 30` с): дефолт OTel SDK рассчитан на миллисекунды и сложил бы все
измерения кита в первый bucket.

### Значения label `result`

`authn_jwt_verifications_total`:

| `result` | Когда |
|---|---|
| `ok` | токен валиден, подпись, сроки и (если запрошен) тип прошли |
| `expired` | сработал `exp` с учётом leeway (`ErrTokenExpired`) |
| `signature_invalid` | подпись не сошлась (`ErrTokenSignature`) |
| `wrong_type` | `ParseAccess`/`ParseRefresh` получили токен другого типа (`ErrWrongTokenType`) |
| `audience_mismatch` | не совпал ожидаемый `iss`/`aud` (`ErrTokenAudience`) |
| `malformed` | битый токен / не-HS256 alg / нет `exp` / `nbf`,`iat` в будущем / payload не объект (`ErrTokenMalformed`) |

`authn_totp_verifications_total`:

| `result` | Когда |
|---|---|
| `ok` | код принят |
| `invalid` | код отвергнут |
| `secret_invalid` | секрет не читается как base32 (порча данных, не ошибка ввода) |
| `replay` | `VerifyOnce`: код уже был принят в своём окне |
| `store_error` | `VerifyOnce`: `UsedCodeStore` недоступен |

## Что эмитится, а что нет

- `Hash`, `Verify` и `VerifyDummy` пишут гистограмму **на каждый вызов**,
  независимо от исхода (запись в `defer`). Метки исхода у них нет — гистограмма
  меряет только время, включая ожидание слота вычислений (не больше
  `max(1, GOMAXPROCS/Threads)` одновременно): рост времени при той же стоимости
  одного хэша — очередь на наплыве логинов.
- `authn_jwt_verifications_total` инкрементится **только при разборе**; `Sign*`
  метрик не пишет.
- `authn_totp_verifications_total` инкрементится **только при проверке кода**
  (ровно один раз на вызов `Verify`/`VerifyOnce`); `Generate` метрик не пишет.
- RBAC (`Registry`) метрик не эмитит.

## Инициализация

Инструменты создаются в конструкторах (`NewArgon2idHasher`, `NewHS256JWT`,
`NewTOTP`) через глобальный `otel.Meter`; повторные конструкторы получают те же
инструменты (OTel SDK дедуплицирует по имени/описанию/unit). Глобальный провайдер
OTel делегирующий: `SetMeterProvider` после конструктора тоже подхватывает уже
созданные инструменты. Без провайдера — no-op.

Resource attrs (`service.name` и пр.) несёт OTel-resource, настроенный
потребителем в его проводке OTel SDK; в самом ките их выставлять не нужно.
