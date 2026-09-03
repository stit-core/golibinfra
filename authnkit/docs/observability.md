# authnkit — observability

Единый источник правды по сигналам кита. Имена/типы/labels берутся из
`metrics.go` и точек записи в `jwt.go` / `hasher.go` / `totp.go`.

Meter: `git.stit.tech/stit-core/golibinfra/authnkit` (через `internal/otelkit`).
Спанов кит не эмитит.

## Метрики

| Метрика | Тип | Unit | Labels | Назначение |
|---|---|---|---|---|
| `jwt_verifications_total` | Int64 Counter | — | `result` | Исход разбора JWT в `HS256JWT.Parse`. |
| `password_hash_duration_seconds` | Float64 Histogram | `s` | — | Время одного `Argon2idHasher.Hash`. |
| `password_verify_duration_seconds` | Float64 Histogram | `s` | — | Время одного `Argon2idHasher.Verify`. |
| `totp_verifications_total` | Int64 Counter | — | `result` | Исход `TOTPGenerator.Verify`. |

### Значения label `result`

`jwt_verifications_total`:

| `result` | Когда |
|---|---|
| `ok` | токен валиден, подпись и сроки прошли |
| `expired` | сработал `exp` с учётом leeway (`ErrTokenExpired`) |
| `signature_invalid` | подпись не сошлась (`ErrTokenSignature`) |
| `malformed` | битый токен / не-HS256 alg / нет `exp` / `nbf`,`iat` в будущем / payload не объект (`ErrTokenMalformed`) |

`totp_verifications_total`:

| `result` | Когда |
|---|---|
| `ok` | код принят |
| `invalid` | код отвергнут (в т.ч. нечитаемый секрет) |

## Что эмитится, а что нет

- `Hash` и `Verify` пишут гистограмму **на каждый вызов**, независимо от исхода
  (запись в `defer`). Метки исхода у них нет — гистограмма меряет только время.
- `jwt_verifications_total` инкрементится **только в `Parse`**; `Sign` метрик не
  пишет.
- `totp_verifications_total` инкрементится **только в `Verify`**; `Generate`
  метрик не пишет.
- RBAC (`Registry`) метрик не эмитит.

## Инициализация

Инструменты создаются в конструкторах (`NewArgon2idHasher`, `NewHS256JWT`,
`NewTOTP`) через глобальный `otel.Meter`; повторные конструкторы получают те же
инструменты (OTel SDK дедуплицирует по имени/описанию/unit). Глобальный провайдер
OTel делегирующий: `SetMeterProvider` после конструктора тоже подхватывает уже
созданные инструменты. Без провайдера — no-op.

Resource attrs (`service.name` и пр.) несёт OTel-resource, настроенный
потребителем в его проводке OTel SDK; в самом ките их выставлять не нужно.
