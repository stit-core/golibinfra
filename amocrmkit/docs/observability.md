# amocrmkit — observability

Единый источник правды по телеметрии kit'а. Инструменты создаются один раз в
`newInstruments()` через meter `git.stit.tech/stit-core/golibinfra/amocrmkit`.
Ошибка создания инструмента — `panic` (контракт нарушения init, как в остальных
китах). Все метрики снимаются в общем пути `execute`, поэтому покрывают
REST v4, amoJo, OAuth и Drive одинаково.

Этот kit **не выставляет** resource-атрибуты (`service.name`/`service.version`/
`deployment.environment.name`/`tenant`) — они приходят из `telemetry.Config`
приложения и корп `otel-collector`.

## Метрики

| Имя | Тип | Labels | Когда |
|---|---|---|---|
| `amocrm_api_calls_total` | Int64Counter | `endpoint`, `status` | каждый outbound-вызов (после `httpclient.Do`) |
| `amocrm_api_duration_seconds` | Float64Histogram (unit `s`) | `endpoint` | длительность каждого outbound-вызова |
| `amocrm_rate_limited_total` | Int64Counter | `account` | ответ amoCRM со статусом 429 |
| `amocrm_oauth_refresh_total` | Int64Counter | `status` | каждый OAuth-обмен (`ExchangeCode`/`RefreshToken`) |

### Семантика labels

- **`endpoint`** — стабильная строковая метка вызова, задаётся в коде (не URL).
  Значения по контурам:
  - REST v4: `leads.list`, `leads.create`, `leads.update`; `contacts.list/create/update`,
    `contacts.chats.list/link`; `companies.list/create/update`; `customers.list/create/update`,
    `transactions.list/create`, `transactions.delete`, `customers.bonus_points`;
    `catalogs.list/create/update`, `catalog_elements.list/create/update`;
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
  ошибка).
- **`status`** (для `amocrm_oauth_refresh_total`) — `"success"` либо `"error"`.
- **`account`** (для `amocrm_rate_limited_total`) — `Account.Subdomain` /
  `AmojoTarget.AccountKey` аккаунта.

## Спаны

Kit **не создаёт собственных спанов** (`tracer.Start` в коде нет). Клиентский span
на каждый HTTP-запрос эмитит `httpclient`, поверх которого работает kit; он
наследует контекст вызова через `ctx`. Распределённая трассировка обеспечивается
именно этим вложенным span'ом, без дублирующего слоя в amocrmkit.
