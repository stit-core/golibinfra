# hubkit — observability

Единый источник правды по сигналам кита. Имена/типы/labels берутся из
`sso/metrics.go` и `sync/metrics.go`.

Meter: `git.stit.tech/stit-core/golibinfra/hubkit/sso` и
`git.stit.tech/stit-core/golibinfra/hubkit/sync` (через `internal/otelkit`). Без
зарегистрированного `MeterProvider` инструменты — no-op.

Собственных спанов кит не эмитит: исходящие запросы `hubkit/client` уже
инструментированы `httpclient` (спан `httpclient.Do <METHOD>`, метрики
`http_client_*`), а тик синка — `timerkit` (спан `timerkit.job hubsync.<resource>`,
метрики `timer_job_*`).

## Метрики

| Метрика | Тип | Unit | Labels | Назначение |
|---|---|---|---|---|
| `hub_sso_verifications_total` | Int64 Counter | — | `contour`, `result` | Исход `Verifier.Verify` одного слота. |
| `hub_sso_jwks_refresh_total` | Int64 Counter | — | `contour`, `result` | Фактические загрузки JWKS (без затроттленных промахов). |
| `hub_sync_records_total` | Int64 Counter | — | `resource` | Записи, применённые в проекцию. |
| `hub_sync_errors_total` | Int64 Counter | — | `resource` | Неудачные тики синка. |
| `hub_sync_cursor_seq` | Int64 ObservableGauge | `{seq}` | `resource` | Текущий курсор (`updated_seq`) ресурса. |
| `hub_sync_last_success_timestamp_seconds` | Float64 ObservableGauge | `s` | `resource` | Unix-время последнего успешного catch-up. |
| `hub_sync_lag_seconds` | Float64 ObservableGauge | `s` | `resource` | Время с последнего успешного catch-up. |

### Значения label'ов

`contour`: `employee` | `customer` — контур верификатора, а не токена.

`result` у `hub_sso_verifications_total`:

| `result` | Когда |
|---|---|
| `ok` | подпись, `iss`/`aud`/`exp`/`nbf`, `sub`, `kind` и (у заказчика) `company_id` прошли |
| `malformed` | токен не разобран: не JWS, `alg` вне allowlist'а (только ES256), битая структура |
| `unknown_key` | `kid` токена отсутствует в JWKS даже после перечитывания набора |
| `jwks_error` | JWKS недоступен — верификатор не может судить о подписи (наружу `ErrJWKSUnavailable` → 503) |
| `signature_invalid` | ключ найден, подпись не сошлась |
| `invalid_claims` | `iss`/`aud`/`exp`/`nbf` не прошли, нет `exp`, нет `sub`, у заказчика нет `company_id` |
| `wrong_kind` | токен другого контура в этом слоте |

`result` у `hub_sso_jwks_refresh_total`: `ok` | `error`. Счётчик растёт и без
неизвестных `kid`: набор перечитывается по истечении окна свежести
(`Cache-Control: max-age` ответа JWKS), поэтому под нагрузкой на инстанс
приходится примерно одна загрузка за окно на контур.

`resource` — имя ресурса фида (`companies`, `projects`, `employees`,
`visibility`, `tags`, `entity-tags`, `departments`): конечное множество,
задаётся продуктом при сборке движка.

### Когда серии появляются

- `hub_sync_cursor_seq` наблюдается только после первого чтения курсора: до
  первого тика «0» неотличим от «фид с начала» и врал бы на дашборде.
- `hub_sync_last_success_timestamp_seconds` и `hub_sync_lag_seconds` — после
  первого успешного тика.
- Отмена `ctx` (graceful stop посреди тика) не инкрементирует
  `hub_sync_errors_total` и не считается ошибкой job'а: курсор сохранён,
  catch-up продолжится со следующего запуска. Дедлайн тика
  (`HUB_SYNC_TIMEOUT`) — ошибка.
- Все три `hub_sync_*`-gauge перестают наблюдаться, когда движок закрыт:
  `sync.Module` снимает регистрацию на `OnStop`, а собранный вручную
  `*sync.Engine` освобождает её `Close()`. Иначе meter держал бы движок вечно
  и рисовал серии ресурса, который больше не тикает.

## Логи

`hubkit/sync` пишет через `logkit.Logger` из графа с полями
`component=hubsync`, `resource=<имя ресурса>`. Единственное предупреждение движка —
`sync cursor did not advance`: фид вернул элементы, но не продвинул курсор
(защита от зацикливания, тик завершается).

`sso.RegisterIdentityLogAttr()` (вызывается `sso.Module`) добавляет в каждую
запись лога поле `user_id` — `employee_id` сессии, а при его отсутствии `sub`.

## Health-проб нет — осознанно

Кит не отдаёт `healthkit.Probe`. Верификатор переживает простой хаба на
загруженном наборе ключей, а `hubkit/client` при недоступном хабе отдаёт
`ErrUnavailable`, по которому продукт деградирует на локальную проекцию.
Проба, завязанная на доступность хаба, выводила бы продукт из ротации там, где
он обязан продолжать обслуживать запросы. Готовность JWKS при необходимости
проверяется вручную — `Verifier.Refresh(ctx)` на старте или в собственной пробе
потребителя.
