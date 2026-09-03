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
| `Config` | настройки уровня кита (см. [configuration.md](configuration.md)); `Validate()` | передаётся в `New` / читается `NewConfig` |
| `NewConfig() (Config, error)` | читает `Config` из ENV + `Validate` | `fx.Provide(amocrmkit.NewConfig)` |
| `Client` | единая точка вызовов всех контуров | один экземпляр на процесс, все аккаунты |

## Координаты вызова

| Тип | Контур | Поля |
|---|---|---|
| `Account` | REST v4 / Drive | `Subdomain` (`^[a-z0-9-]{1,63}$`), `TLD` (`"ru"`/`"com"`), `Token` (Bearer) |
| `AmojoTarget` | amoJo Chat | `TLD` (`"ru"`/`"com"`), `AccountKey` (= `Account.Subdomain`, тот же формат), `Secret` (channel secret) |

OAuth-методы координат-структуры не используют — subdomain/tld/секреты приходят
отдельными параметрами и проходят ту же валидацию. Координаты вне формата →
`ErrInvalidAccount` без сетевого вызова.

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
| leads (`client_leads.go`) | `ListLeads`, `CreateLeads`, `UpdateLeads`, `CreateLeadsComplex`; unsorted: `CreateUnsortedSIP`, `CreateUnsortedForms`, `AcceptUnsorted`, `DeclineUnsorted`, `LinkUnsorted` |
| contacts + companies (`client_contacts.go`) | `ListContacts`, `CreateContacts`, `UpdateContacts`, `ListContactChats`, `LinkContactChat`; `ListCompanies`, `CreateCompanies`, `UpdateCompanies` |
| customers + transactions (`client_customers.go`) | `ListCustomers`, `CreateCustomers`, `UpdateCustomers`, `ListTransactions`, `CreateTransactions`, `DeleteTransaction`, `AdjustBonusPoints` (RU-only), `SetCustomersMode` |
| catalogs + elements (`client_catalogs.go`) | `ListCatalogs`, `CreateCatalogs`, `UpdateCatalogs`, `ListCatalogElements`, `CreateCatalogElements`, `UpdateCatalogElements` |
| entity links (`client_links.go`) | `LinkEntities`, `UnlinkEntities` (матрица допустимых связей проверяется до запроса) |
| tasks (`client_tasks.go`) | `ListTasks`, `CreateTasks`, `UpdateTasks`, `CompleteTask` |
| notes + calls (`client_notes.go`) | `AddNotes`, `AddCalls` |
| talks (`client_talks.go`) | `GetTalk`, `ListTalks`, `CloseTalk` |
| chat templates (`client_chat.go`) | `ListChatTemplates`, `CreateChatTemplates`, `UpdateChatTemplate`, `DeleteChatTemplates`, `SubmitTemplateReview`, `UpdateTemplateReview` |
| files + events (`client_files.go`) | `AttachFiles`, `DetachFiles`, `GetFileLinks`, `ListEvents` |
| account-meta (`client_account.go`) | `GetAmojoID`, `GetDriveURL`, `GetAccountTaskTypes`, `GetEventTypes` |
| account-config (`client_config.go`) | pipelines/statuses: `ListPipelines`, `CreatePipelines`, `UpdatePipeline`, `UpsertStatuses`; custom fields/groups: `ListCustomFields`, `CreateCustomFields`, `UpdateCustomFields`, `DeleteCustomField`, `ListCustomFieldGroups`, `CreateCustomFieldGroups`, `UpdateCustomFieldGroup`, `DeleteCustomFieldGroup`; tags: `ListTags`, `CreateTags`, `UpdateTags`; `ListLossReasons`; sources/buttons/online chat: `ListSources`, `CreateSources`, `UpdateSources`, `DeleteSources`, `ListWebsiteButtons`, `CreateWebsiteButton`, `UpdateWebsiteButton`, `ConnectOnlineChat`; users/roles: `ListUsers`, `CreateUsers`, `ActivateUser`, `DeactivateUser`, `ListRoles`, `CreateRoles`, `UpdateRole`, `DeleteRole`; customer statuses/segments: `ListCustomerStatuses`, `CreateCustomerStatuses`, `UpdateCustomerStatus`, `DeleteCustomerStatus`, `ListSegments`, `CreateSegments`, `UpdateSegment`, `DeleteSegment` |

`GET /{id}`-геттеры есть только у `talks` (`GetTalk`): одиночное чтение прочих
сущностей — из локального read-model потребителя, не read-through к amoCRM.

## Drive — методы (`client_files.go`)

`CreateUploadSession`, `UploadPart` (чанк + `Content-Range`), `PatchFile`,
`TrashFiles`, `RestoreFiles`, `GetFileVersions`, `ListFiles`. Все принимают
`driveURL` параметром (из `GetDriveURL`) и `Account` (валидируется). Типы:
`File`, `UploadSession`, `UploadSessionInput`, `PatchFileInput`, `FileLink`,
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
| `ExchangeCode(ctx, subdomain, tld, clientID, clientSecret, code, redirectURI) (Tokens, error)` | grant `authorization_code` |
| `RefreshToken(ctx, subdomain, tld, clientID, clientSecret, refreshToken, redirectURI) (Tokens, error)` | grant `refresh_token` (`redirect_uri` обязателен) |
| `Tokens` | `AccessToken`, `RefreshToken`, `ExpiresIn`, `ServerTime`; метод `ExpiresAt()` (= `ServerTime + ExpiresIn`, UTC) |
| `TokenSource` | контракт поставщика access-токена (`Token(ctx, accountSubdomain) (string, error)`); кит определяет, но сам не вызывает |

## Ошибки (`errors.go`)

Sentinel'ы для `errors.Is` и маппинг HTTP-статусов:

| Sentinel | Когда |
|---|---|
| `ErrConfig` | невалидный `Config` (`RateRPS <= 0`, `HostOverride` не `scheme://host`) |
| `ErrInvalidAccount` | subdomain/tld вне формата (REST, Drive, OAuth, amoJo) — запрос не отправляется |
| `ErrAuth` | 401 |
| `ErrForbidden` | 403 (в т.ч. IP-бан) |
| `ErrNotFound` | 404 |
| `ErrValidation` | 400, 422, прочие 4xx (402/405/409/…) |
| `ErrRateLimited` | 429 |
| `ErrServer` | 5xx |

`*APIError` (доступен через `errors.As`) несёт `Status`, `Title`, `Type`,
`Detail`, `ValidationErrors []ValidationError`, `RetryAfter` (секунды, поле тела
на 429), `TraceID` (на 500). `ValidationError` → `RequestID` + `[]FieldError`
(`Code`, `Path`, `Detail`).
