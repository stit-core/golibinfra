# amocrmkit — карта API

Экспортируемые типы и методы по контурам. Источник правды — код (`client_*.go`,
`amojo.go`, `oauth.go`, `signing.go`, `errors.go`). Подробные сценарии —
[usage.md](usage.md), устройство — [architecture.md](architecture.md).

## Конструкция и проводка

| Символ | Роль | Когда использовать |
|---|---|---|
| `Module` (`fx.Options`) | provide'ит `*Client` из графа (`Config`, `httpclient.Config`, `clockkit.Clock`, `logkit.Logger`) | подключение через fx |
| `New(cfg, hcCfg, clock, log, opts...) (*Client, error)` | прямая сборка клиента; кит собирает свой `httpclient` из `hcCfg` | вне fx / в тестах |
| `Option`: `WithHTTPClient(*httpclient.Client)`, `WithTransport(http.RoundTripper)` | готовый httpclient (тесты) / базовый транспорт собираемого клиента | см. [configuration.md](configuration.md) |
| `Config` | настройки уровня кита (см. [configuration.md](configuration.md)); `(*Config).Validate()` | передаётся в `New` / читается `NewConfig` |
| `NewConfig() (Config, error)` | читает `Config` из ENV + `Validate` | `fx.Provide(amocrmkit.NewConfig)` |
| `Client` | единая точка вызовов всех контуров | один экземпляр на процесс, все аккаунты |

## Координаты вызова

| Тип | Контур | Поля |
|---|---|---|
| `Account` | REST v4 / Drive | `Subdomain` (`^[a-z0-9-]{1,63}$`), `TLD` (`"ru"`/`"com"`), `Token` (Bearer) |
| `AmojoTarget` | amoJo Chat | `TLD` (`"ru"`/`"com"`), `AccountKey` (= `Account.Subdomain`, тот же формат), `Secret` (channel secret) |

OAuth-методы принимают тот же `Account` (поле `Token` не используется) и
`OAuthConfig` с параметрами интеграции. Координаты вне формата →
`ErrInvalidAccount` без сетевого вызова; динамические сегменты пути (`uid`,
`uuid`, id групп, `scope_id`, `msgid`) экранируются, имена сущностей в пути
проверяются по allowlist, а пустой сегмент и dot-сегменты (`.`, `..`) в собранном
пути отвергаются (`ErrInvalidRequest`).

Все идентификаторы сущностей — `int64` (поля моделей и параметры методов).

## Пагинация и общие параметры (REST v4)

| Тип | Роль |
|---|---|
| `Page[T]` | результат list-методов: `Page`, `TotalItems`, `Links`, `Items []T` |
| `ListParams` | query list-методов: `Page`, `Limit` (≤250), `With`, `Query`, `Filter`, `Order` |
| `Links` / `Link` | HAL-навигация `_links` (`Self`/`Next`/`Prev`/`First`/`Last`) |

## Shared wire-типы (`types.go`)

`CustomFieldValue` / `CustomFieldValueItem` (значения полиморфны — `any`),
`CustomFieldEdit` + `CustomFieldValues`, `CodeField`, `Tag` / `TagRef`,
`CreatedEntity` (`ID`, `RequestID`, `UpdatedAt`, `Links`), `ContactRef` /
`CompanyRef`.

## REST v4 — методы по доменам

Каждый домен экспортирует пару `<Entity>` (GET-объект) / `<Entity>Input`
(тело POST/PATCH). `Create*`/`Update*` — batch (срез на вход, у большинства до 250;
у шаблонов/воронок/тегов/источников/пользователей/статусов лимит ниже — см.
комментарии методов).

| Домен (файл) | Методы |
|---|---|
| leads (`client_leads.go`) | `GetLead`, `ListLeads`, `CreateLeads`, `UpdateLeads`, `CreateLeadsComplex`; unsorted: `CreateUnsortedSIP`, `CreateUnsortedForms`, `AcceptUnsorted`, `DeclineUnsorted`, `LinkUnsorted` |
| contacts + companies (`client_contacts.go`) | `GetContact`, `ListContacts`, `CreateContacts`, `UpdateContacts`, `ListContactChats`, `LinkContactChat`; `GetCompany`, `ListCompanies`, `CreateCompanies`, `UpdateCompanies` |
| customers + transactions (`client_customers.go`) | `GetCustomer`, `ListCustomers`, `CreateCustomers`, `UpdateCustomers`, `ListTransactions`, `CreateTransactions`, `DeleteTransaction`, `AdjustBonusPoints` (RU-only), `SetCustomersMode` |
| catalogs + elements (`client_catalogs.go`) | `ListCatalogs`, `CreateCatalogs`, `UpdateCatalogs`, `ListCatalogElements`, `CreateCatalogElements`, `UpdateCatalogElements` |
| entity links (`client_links.go`) | `LinkEntities`, `UnlinkEntities` (матрица допустимых связей проверяется до запроса; связь асимметрична: `companies→leads` допустима, `contacts→leads` — нет) |
| tasks (`client_tasks.go`) | `GetTask`, `ListTasks`, `CreateTasks`, `UpdateTasks`, `CompleteTask` |
| notes + calls (`client_notes.go`) | `AddNotes`, `AddCalls` |
| talks (`client_talks.go`) | `GetTalk`, `ListTalks`, `CloseTalk` |
| chat templates (`client_chat.go`) | `ListChatTemplates`, `CreateChatTemplates`, `UpdateChatTemplate`, `DeleteChatTemplates`, `SubmitTemplateReview`, `UpdateTemplateReview` |
| files + events (`client_files.go`) | `AttachFiles`, `DetachFiles`, `GetFileLinks`, `ListEvents` |
| account-meta (`client_account.go`) | `GetAmojoID`, `GetDriveURL`, `GetAccountTaskTypes`, `GetEventTypes` |
| account-config (`client_config.go`) | pipelines/statuses: `ListPipelines`, `CreatePipelines`, `UpdatePipeline`, `UpsertStatuses`; custom fields/groups: `ListCustomFields`, `CreateCustomFields`, `UpdateCustomFields`, `DeleteCustomField`, `ListCustomFieldGroups`, `CreateCustomFieldGroups`, `UpdateCustomFieldGroup`, `DeleteCustomFieldGroup`; tags: `ListTags`, `CreateTags`, `UpdateTags`; `ListLossReasons`; sources: `ListSources`, `CreateSources`, `UpdateSources`, `DeleteSources`; CRM Plugin (`/api/v4/website_buttons`): `ListWebsiteButtons`, `GetWebsiteButton`, `CreateWebsiteButton`, `UpdateWebsiteButton`, `ConnectOnlineChat`; users/roles: `ListUsers`, `CreateUsers`, `ActivateUsers`, `DeactivateUsers`, `ListRoles`, `CreateRoles`, `UpdateRole`, `DeleteRole`; customer statuses/segments: `ListCustomerStatuses`, `CreateCustomerStatuses`, `UpdateCustomerStatus`, `DeleteCustomerStatus`, `ListSegments`, `CreateSegments`, `UpdateSegment`, `DeleteSegment` |

`GET /{id}`-геттеры (`GetLead`, `GetContact`, `GetCompany`, `GetCustomer`,
`GetTask`, `GetTalk`, `GetWebsiteButton`) принимают вариадик `with ...string`.
Пустой ответ на такой запрос — `ErrNotFound` (для списков и батчей пустой ответ —
просто «нет данных»).

## Drive — методы (`client_files.go`)

| Метод | Роль |
|---|---|
| `UploadFile(ctx, acc, driveURL, in, io.Reader) (File, error)` | файл целиком: сессия + части по `MaxPartSize` по цепочке `next_url`; в памяти живёт не больше одной части |
| `CreateUploadSession` → `UploadSession` | POST `{drive_url}/v1.0/sessions` |
| `UploadPart(ctx, acc, uploadURL, contentRange, chunk) (File, error)` | одна часть; промежуточный ответ несёт `File.NextURL`/`File.SessionID`, финальный — `UUID` |
| `PatchFile`, `TrashFiles`, `RestoreFiles`, `GetFileVersions`, `ListFiles` | операции над файлами Drive |

Все принимают `driveURL` (из `GetDriveURL`) и `Account`. Адрес Drive должен
лежать на `https://*.amocrm.<TLD>` аккаунта, иначе `ErrInvalidAccount`: Bearer не
уходит на чужой хост (проверка выключена при `HostOverride`). Типы: `File`,
`UploadSession`, `UploadSessionInput`, `PatchFileInput`, `FileLink`,
`AttachFileInput`.

## amoJo Chat — методы (`amojo.go`)

| Метод | Эндпоинт / событие |
|---|---|
| `AmojoConnect` → `ConnectResult` | POST `.../{channel_id}/connect` (`hook_api_version` дефолтит в `"v2"`) |
| `AmojoDisconnect` | DELETE `.../{channel_id}/disconnect` |
| `AmojoCreateChat` → `CreateChatResult` | POST `.../{scope_id}/chats` |
| `AmojoNewMessage` → `NewMessageResult` | POST `.../{scope_id}` (event_type=new_message; `silent` → send/import) |
| `AmojoEditMessage` → `NewMessageResult` | POST `.../{scope_id}` (event_type=edit_message) |
| `AmojoReact` | POST `.../{scope_id}/react` (тело `map[string]any`) |
| `AmojoTyping` | POST `.../{scope_id}/typing` (тело `map[string]any`) |
| `AmojoDeliveryStatus` | POST `.../{scope_id}/{msgid}/delivery_status` (тело `map[string]any`) |

Типы: `ConnectInput`, `CreateChatInput`, `NewMessagePayload`, `AmojoParticipant`,
`AmojoProfile`. Полиморфные тела (message/react/typing) — `map[string]any`: строит
потребитель.

## Подпись amoJo (`signing.go`)

| Символ | Роль |
|---|---|
| `VerifyWebhookSignature(rawBody, receivedSignature, secret) bool` | проверка **входящего** вебхука: HMAC-SHA1 по сырому телу, голый hex без `sha1=`, сравнение в постоянное время |

Подпись **исходящих** запросов считается внутри `amojoCall` и наружу не
экспортируется.

## OAuth2 (`oauth.go`)

| Символ | Роль |
|---|---|
| `OAuthConfig` | `ClientID`, `ClientSecret`, `RedirectURI` (обязателен в теле обоих grant'ов) |
| `ExchangeCode(ctx, acc, app, code) (TokenPair, error)` | grant `authorization_code` |
| `RefreshToken(ctx, acc, app, refreshToken) (TokenPair, error)` | grant `refresh_token` |
| `TokenPair` | `AccessToken`, `RefreshToken`, `ExpiresIn`, `ServerTime`; метод `ExpiresAt()` (= `ServerTime + ExpiresIn`, UTC). Если amoCRM не прислал `server_time`, кит подставляет свои часы |
| `TokenStore` | контракт потребителя: `Load(ctx, acc) (TokenPair, error)`, `Save(ctx, acc, TokenPair) error` |
| `NewRefreshCoordinator(client, store, app, opts...) *RefreshCoordinator` | сериализация refresh на аккаунт; опция `WithRefreshSkew(d)` (дефолт 5 минут) |
| `RefreshCoordinator.Token(ctx, acc) (string, error)` | актуальный access-токен: отдаёт сохранённый либо обновляет пару |
| `RefreshCoordinator.Refresh(ctx, acc) (TokenPair, error)` | принудительное обновление; параллельные вызовы одного аккаунта делят один обмен |

Refresh-токен amoCRM **одноразовый**: после обмена старый недействителен, новая
пара сохраняется до возврата вызывающему. Два параллельных обмена одной парой
теряют интеграцию — используйте `RefreshCoordinator` (или собственный
single-flight поверх `TokenStore`).

## Ошибки (`errors.go`)

Sentinel'ы для `errors.Is` и маппинг HTTP-статусов:

| Sentinel | Когда | Класс |
|---|---|---|
| `ErrConfig` | невалидный `Config` кита или переданный `httpclient.Config` | старт |
| `ErrInvalidAccount` | subdomain/tld вне формата; адрес Drive вне доменов amoCRM — запрос не отправляется | перманентный |
| `ErrInvalidRequest` | отказ кита до сети: сегмент пути вне allowlist, пустой или dot-сегмент в пути, связь вне матрицы, невалидный URL, несериализуемое тело | перманентный |
| `ErrAuth` | 401 | нужен refresh/переавторизация |
| `ErrPaymentRequired` | 402 — подписка аккаунта закончилась или функционал не оплачен | пауза аккаунта |
| `ErrForbidden` | 403 — нет прав либо аккаунт заблокирован за превышение rps | пауза аккаунта |
| `ErrNotFound` | 404 либо пустой ответ на запрос одиночной сущности | перманентный |
| `ErrValidation` | 400, 422, прочие 4xx (405/409/…) | перманентный |
| `ErrRateLimited` | 429 от amoCRM **или** локальный отказ лимитера (пауза после 429 / очередь дольше `MaxWait`) | повтор позже |
| `ErrServer` | 5xx | повтор позже |
| `ErrResponseTooLarge` | тело успешного ответа больше 32 MiB | перманентный |

`*APIError` (доступен через `errors.As`) несёт `Status`, `Title`, `Type`,
`Detail`, `ValidationErrors []ValidationError`, `RetryAfter` (секунды: поле тела
`retry_after` либо заголовок `Retry-After` на 429 в любой форме RFC 9110 —
секунды или HTTP-дата; значение обрезается сутками), `TraceID` (на 500).
`ValidationError` → `RequestID` + `[]FieldError` (`Code`, `Path`, `Detail`).
