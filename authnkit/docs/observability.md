# authnkit — observability

Единый источник правды по сигналам кита. Имена/типы/labels берутся из
`metrics.go` и точек записи в `jwt.go` / `hasher.go` / `totp.go`, у сессий —
`session/pgstore/metrics.go` и `session/pgstore/store.go`.

Meter: `git.stit.tech/stit-core/golibinfra/authnkit` (через `internal/otelkit`).
Спанов кит не эмитит; `ctx` в сигнатурах нужен, чтобы измерения несли baggage и
exemplar'ы вызывающего.

## Метрики

| Метрика | Тип | Unit | Labels | Назначение |
|---|---|---|---|---|
| `authn_jwt_verifications_total` | Int64 Counter | — | `result` | Исход разбора JWT в `HS256JWT.Parse`/`ParseAccess`. |
| `authn_password_busy_total` | Int64 Counter | — | — | Отказы `ErrBusy` у `Argon2idHasher.Hash`, `Verify` и `VerifyDummy`: очередь вычислений полна. |
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
| `wrong_type` | `ParseAccess` получил токен без `typ=access` (`ErrWrongTokenType`) |
| `audience_mismatch` | не совпал ожидаемый `iss`/`aud` (`ErrTokenAudience`) |
| `malformed` | битый токен / не-HS256 alg / нет `exp` / `nbf`,`iat` в будущем / payload не объект (`ErrTokenMalformed`) |

`authn_totp_verifications_total`:

| `result` | Когда |
|---|---|
| `ok` | код принят (у `VerifyOnce` — впервые в своём окне) |
| `invalid` | код отвергнут |
| `secret_invalid` | секрет не читается как base32 (порча данных, не ошибка ввода) |
| `replay` | `VerifyOnce`: код уже был принят в своём окне |
| `store_error` | `VerifyOnce`: `UsedCodeStore` недоступен |

## Сессии refresh (`session/pgstore`)

Meter: `git.stit.tech/stit-core/golibinfra/authnkit/session`. Пишет хранилище
`pgstore.Store`; фейк `testkit/sessiontest` метрик не эмитит.

| Метрика | Тип | Unit | Labels | Назначение |
|---|---|---|---|---|
| `authn_session_issued_total` | Int64 Counter | — | `result` | Исход `Issue`: `ok`, `invalid` (`ErrInvalidSubject`), `error`. |
| `authn_session_rotations_total` | Int64 Counter | — | `result` | Исход `Rotate`: `ok`, `retry` (повтор в окне `SESSION_RETRY_WINDOW`: новый преемник, прежний погашен), `invalid` (неизвестный токен или отозванная сессия), `expired`, `reused` (прочий повтор использованного токена — сессия отозвана), `error` (в т.ч. `ErrInTx`). |
| `authn_session_revoked_total` | Int64 Counter | — | `reason` | Закрытые сессии: `logout` (`Revoke`), `all` (`RevokeAll`), `reused` (отзыв при повторе токена). Значение — число сессий. |
| `authn_session_cleanup_deleted_total` | Int64 Counter | — | — | Сессии с истёкшим абсолютным сроком, удалённые `Cleanup`. |

Рост `reused` — признак кражи refresh-токенов или клиента, который обновляет
параллельно: алерт на него ставится первым. Одиночный повтор клиента, чей ответ
потерян, в окне повтора — `retry`, не `reused`; рост `retry` без сбоев сервиса —
сбои сети у клиентов или повторы украденных токенов в окне.

Счётчики пишутся в момент вызова: `Issue` и `RevokeAll` в транзакции, которую
вызывающий потом откатил, оставляют `issued_total{result="ok"}` и
`revoked_total{reason="all"}` — коммита кит не видит.

## Что эмитится, а что нет

- `Hash`, `Verify` и `VerifyDummy` пишут гистограмму **на каждое вычисление,
  принятое в очередь**, — от допуска до результата, включая ожидание слота (не
  больше `max(1, GOMAXPROCS/Threads)` вычислений одновременно) и исход отмены `ctx`
  в очереди. Метки исхода у неё нет: рост времени при той же стоимости одного хэша —
  очередь на наплыве логинов. Без записи — отказ до вычисления: пароль длиннее
  предела у `Hash`, нечитаемая PHC-строка у `Verify` и отказ `ErrBusy`.
- `authn_password_busy_total` — по одному на отказ `ErrBusy` (слот ждут восемь
  вызовов на слот, очередь полна). Ненулевой темп — хэшеру не хватает ёмкости:
  поток входов сильнее неё или мало ядер на `ARGON2_THREADS`; у `Verify` пароля
  длиннее предела отказ считает его холостое вычисление.
- `authn_jwt_verifications_total` инкрементится **только при разборе**; `Sign`
  и `SignAccess` метрик не пишут.
- `authn_totp_verifications_total` инкрементится **только при проверке кода**
  (ровно один раз на вызов `Verify`/`VerifyOnce` — итогом: повтор кода — `replay`, а не `ok`
  и `replay`); `Generate` метрик не пишет.
- RBAC (`Registry`) метрик не эмитит.

## Инициализация

Инструменты создаются в конструкторах (`NewArgon2idHasher`, `NewHS256JWT`,
`NewTOTP`) через глобальный `otel.Meter`; повторные конструкторы получают те же
инструменты (OTel SDK дедуплицирует по имени/описанию/unit). Глобальный провайдер
OTel делегирующий: первый `SetMeterProvider` после конструктора тоже подхватывает
уже созданные инструменты. Без провайдера — no-op.

Resource attrs (`service.name` и пр.) несёт OTel-resource, настроенный
потребителем в его проводке OTel SDK; в самом ките их выставлять не нужно.
