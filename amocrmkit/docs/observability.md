# amocrmkit — observability

Единый источник правды по телеметрии кита. Инструменты создаются один раз в
`newInstruments()` через meter `git.stit.tech/stit-core/golibinfra/amocrmkit`
(`internal/otelkit`). Ошибка создания инструмента — `panic` (контракт нарушения
init, как в остальных китах). Все метрики снимаются в общем пути `execute`,
поэтому покрывают REST v4, amoJo, OAuth и Drive одинаково.

Этот кит **не выставляет** resource-атрибуты (`service.name`/`service.version`/
`deployment.environment.name`) — их несёт OTel-resource, настроенный потребителем
в его проводке OTel SDK; без зарегистрированного `MeterProvider` метрики — no-op.

## Метрики

| Имя | Тип | Labels | Когда |
|---|---|---|---|
| `amocrm_api_calls_total` | Int64Counter | `endpoint`, `status` | каждый outbound-вызов (после `httpclient.Do`, ретраи httpclient внутри одного вызова не размножают счётчик) |
| `amocrm_api_duration_seconds` | Float64Histogram (unit `s`) | `endpoint` | длительность каждого outbound-вызова, включая ретраи httpclient и ожидание слота лимитера при ретрае |
| `amocrm_rate_limited_total` | Int64Counter | `account` | ответ amoCRM со статусом 429 |
| `amocrm_oauth_refresh_total` | Int64Counter | `status` | каждый OAuth-обмен (`ExchangeCode`/`RefreshToken`) |

Ожидание слота лимитера **перед первой** попыткой в длительность не входит —
оно происходит до `httpclient.Do`. Вызов с невалидными координатами
(`ErrInvalidAccount`) метрик не пишет: HTTP-запроса не было.

### Семантика labels

- **`endpoint`** — стабильная строковая метка вызова, задаётся в коде (не URL).
  Значения по контурам:
  - REST v4: `leads.list`, `leads.create`, `leads.update`, `leads.complex`;
    `contacts.list/create/update`, `contacts.chats.list/link`;
    `companies.list/create/update`; `customers.list/create/update`,
    `transactions.list/create`, `transactions.delete`, `customers.bonus_points`,
    `customers.mode`; `catalogs.list/create/update`,
    `catalog_elements.list/create/update`; `entity.link`, `entity.unlink`;
    `tasks.list/create/update`, `tasks.complete`; `notes.add`, `calls.add`;
    `talks.get/list/close`; `chat_templates.list/create/update/delete`,
    `chat_templates.review.submit`, `chat_templates.review.update`;
    `pipelines.list/create/update`, `statuses.upsert`;
    `custom_fields.list/create/update/delete`,
    `custom_field_groups.list/create/update/delete`; `tags.list/create/update`;
    `loss_reasons.list`; `sources.list/create/update/delete`,
    `website_buttons.list/create/update`, `online_chat.connect`;
    `users.list/create`, `users.activate`, `users.deactivate`,
    `roles.list/create/update/delete`;
    `customer_statuses.list/create/update/delete`, `segments.list/create/update/delete`;
    `account.get`, `events.types`; `files.attach/detach/links`, `events.list`;
    `unsorted.sip/forms/accept/decline/link`.
  - Drive: `drive.session.create`, `drive.upload`, `drive.patch`, `drive.trash`,
    `drive.restore`, `drive.versions`, `drive.list`.
  - amoJo: `amojo.connect`, `amojo.disconnect`, `amojo.create_chat`,
    `amojo.new_message`, `amojo.edit_message`, `amojo.react`, `amojo.typing`,
    `amojo.delivery_status`.
  - OAuth: `oauth.exchange`, `oauth.refresh`.
- **`status`** (для `amocrm_api_calls_total`) — HTTP-код как строка (`"200"`,
  `"404"`, …) либо `"error"`, если `httpclient` не вернул ответ (транспортная
  ошибка, открытый breaker, отмена ctx).
- **`status`** (для `amocrm_oauth_refresh_total`) — `"success"` либо `"error"`.
- **`account`** (для `amocrm_rate_limited_total`) — `Account.Subdomain` /
  `AmojoTarget.AccountKey` аккаунта.

## Спаны

Кит **не создаёт собственных спанов** (`tracer.Start` в коде нет). Клиентский span
на каждую HTTP-попытку эмитит `httpclient` (otelhttp-транспорт), поверх которого
работает кит; он наследует контекст вызова через `ctx`. Распределённая
трассировка обеспечивается именно этим вложенным span'ом, без дублирующего слоя
в amocrmkit.

## Логи

`New(..., log logkit.Logger, ...)` (`nil` → `logkit.Nop()`). Кит пишет только
про сбои — успешные вызовы видны в метриках и спанах:

| Уровень | Сообщение | Когда | Поля |
|---|---|---|---|
| `Warn` | `amocrmkit: rate limited` | amoCRM ответил 429 | `endpoint`, `account`, `retry_after` (секунды из тела/заголовка) |
| `Warn` | `amocrmkit: server error` | ответ 5xx | `endpoint`, `account`, `status`, `trace_id` |
| `Warn` | `amocrmkit: request failed` | `httpclient.Do` вернул ошибку (транспорт, breaker, ctx) | `endpoint`, `account`, `error` |
| `Warn` | `amocrmkit: oauth failed` | `ExchangeCode`/`RefreshToken` завершились ошибкой любого рода | `grant`, `account`, `error` |
