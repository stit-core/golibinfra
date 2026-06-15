# authnkit — observability

Единый источник правды по сигналам кита. Имена/типы/labels берутся из
`metrics.go` и точек записи в `jwt.go` / `hasher.go` / `totp.go`.

Meter: `git.stit.tech/stit-core/golibinfra/authnkit`. Спанов кит не эмитит.

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
| `ok` | токен валиден, подпись и срок прошли |
| `expired` | сработал `exp` (мапится в `ErrTokenExpired`) |
| `signature_invalid` | подпись не сошлась (`ErrTokenSignature`) |
| `malformed` | битый токен / не-HMAC alg / claims не `MapClaims` (`ErrTokenMalformed`) |

`totp_verifications_total`:

| `result` | Когда |
|---|---|
| `ok` | код принят |
| `invalid` | код отвергнут |

## Что эмитится, а что нет

- `Hash` и `Verify` пишут гистограмму **на каждый вызов**, независимо от исхода
  (запись в `defer`). Метки исхода у них нет — гистограмма меряет только время.
- `jwt_verifications_total` инкрементится **только в `Parse`**; `Sign` метрик не
  пишет.
- `totp_verifications_total` инкрементится **только в `Verify`**; `Generate`
  метрик не пишет.
- RBAC (`Has`, `RegisterRolePermissions`) метрик не эмитит.

## Инвариант инициализации

Инструменты создаются один раз через `sync.Once` (`getInstruments`) и
привязываются к тому `MeterProvider`, который активен на момент **первого**
вызова любой функции кита. Установи `MeterProvider` до первого хэша/подписи/
верификации — иначе сигналы уйдут в no-op provider и на дашбордах их не будет. В
fx-сборке это гарантирует подключённый `telemetry.Module`.

Все labels автоматически дополняются resource attrs корп otel-collector'ом
(`resource_to_telemetry_conversion: true`) — `service.name`, `tenant` и пр.
выставлять в самом ките не нужно.
