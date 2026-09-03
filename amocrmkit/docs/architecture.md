# amocrmkit — архитектура

## Назначение и место в стеке

`amocrmkit` — транспортный слой между сервисом-потребителем и amoCRM. Он
инкапсулирует четыре протокольных контура amoCRM и не более того:

- **REST API v4** — `https://<subdomain>.amocrm.<tld>/api/v4/...`, аутентификация
  Bearer-токеном.
- **amoJo Chat API** — `https://amojo.amocrm.<tld>/v2/origin/custom/...`,
  аутентификация подписью HMAC-SHA1 по каждому запросу.
- **OAuth2** — `https://<subdomain>.amocrm.<tld>/oauth2/access_token`, обмен code и
  refresh токенов.
- **Drive** — динамический `drive_url` аккаунта (резолвится через `GetDriveURL`),
  Bearer, чанковая загрузка файлов.

Кит **stateless**: не хранит токены, не ведёт persistence, не общается с
очередями, не содержит бизнес-логики и не оркестрирует мультиаккаунт. Всё это —
ответственность потребителя. Один экземпляр `Client` обслуживает все аккаунты.

## Per-account, а не per-instance

Координаты аккаунта приходят аргументами вызовов, не глобальной конфигурацией:

- REST/Drive — структура `Account{Subdomain, TLD, Token}`.
- amoJo — структура `AmojoTarget{TLD, AccountKey, Secret}`.
- OAuth — subdomain/tld/client_id/client_secret/code/refresh передаются отдельными
  параметрами методов.

Выбор аккаунта и хранение токенов (token-store) — у потребителя. `Config` несёт
только то, что одинаково для всех аккаунтов.

`.ru` и `.com` — разные домены и невзаимозаменяемы; TLD выбирает потребитель по
аккаунту.

### Валидация координат

Subdomain и TLD попадают в host запроса, а с ним уходят Bearer-токен и
`client_secret`. Поэтому каждый путь, который строит URL (REST, Drive, OAuth,
amoJo), сначала проверяет `Subdomain`/`AccountKey` по `^[a-z0-9-]{1,63}$` и
`TLD ∈ {ru, com}`; несоответствие → `ErrInvalidAccount` без сетевого вызова.
Server-provided адреса Drive (`drive_url`, `upload_url`) проверяются на
абсолютный URL с хостом.

## Компоненты

| Компонент | Файл | Роль |
|---|---|---|
| `Client` | `client.go` | единая точка: собирает `httpclient` с хуком лимитера, держит rate-limiter и OTel-инструменты; `Account`, валидация, `resolveURL`, `execute` |
| `accountLimiter` | `ratelimit.go` | per-account token-bucket (`golang.org/x/time/rate`) с TTL-вытеснением простаивающих аккаунтов и хуком `beforeAttempt` для ретраев |
| `instruments` | `metrics.go` | счётчики/гистограмма OTel через `internal/otelkit` |
| таксономия ошибок | `errors.go` | sentinel'ы `Err*` + `*APIError` |
| подпись amoJo | `signing.go` | исходящая 5-компонентная подпись + верификация вебхука |
| OAuth | `oauth.go` | `ExchangeCode`/`RefreshToken`, `Tokens`, `TokenSource` |
| amoJo Chat | `amojo.go` | channel lifecycle, chats, messages, react/typing/delivery |
| REST-домены | `client_*.go` | leads/contacts/companies/customers/catalogs/links/tasks/notes/talks/chat/files/config/account |
| shared wire-типы | `types.go` | `CustomFieldValue`, `Tag`/`TagRef`, `CreatedEntity`, `ContactRef`/`CompanyRef` |

## Поток одного REST-вызова

Все REST/Drive-методы сводятся к нескольким generic-обёрткам, которые строят
`restReq` и зовут `doREST` → `execute`:

1. `doREST` валидирует `Account`, собирает URL (через `resolveURL`, учитывая
   `HostOverride`), маршалит тело в JSON, ставит заголовки (`Authorization:
   Bearer`, `Accept`, при теле — `Content-Type`).
2. `execute` — общий путь для всех контуров:
   - кладёт аккаунт в `ctx` (для хука лимитера) и ждёт слот
     `accountLimiter.wait(ctx, account)` — до свободного слота либо до отмены ctx;
   - `httpclient.Do` (retry + circuit-breaker + клиентский OTel-span); каждая
     повторная попытка httpclient проходит через `accountLimiter.beforeAttempt`;
   - запись метрик `amocrm_api_calls_total` и `amocrm_api_duration_seconds`;
   - при `>= 400` — `classify(status, parseAPIError(resp))`; для 429 дополнительно
     инкремент `amocrm_rate_limited_total` и `Warn`-лог, для 5xx — `Warn`-лог;
   - `204` / пустое тело → `out` не трогается, ошибки нет;
   - иначе декод JSON в `out`.

### Generic-обёртки (`client.go`)

| Обёртка | Назначение |
|---|---|
| `listEntities[T]` | GET list → `embeddedPage[T]` по `_embedded[key]`, возвращает `Page[T]` |
| `getOne[T]` | GET одиночного объекта (`/{id}`, `/account`) |
| `sendBatch[In,Out]` | POST/PATCH массива, достаёт `_embedded[key]` как `[]Out` |
| `sendOne[Out]` | POST/PATCH одиночного объекта, декодит ответ в `Out` |
| `sendNoContent` | вызовы без полезного тела ответа (delete/detach/accept/202/204) |
| `embeddedPage[T]` | строит `Page[T]` из декодированного конверта (list и Drive-методы) |

`Page[T]` несёт `Page`, `TotalItems`, `Links` (HAL `_links`) и `Items`. Конверт
list/batch-ответов REST v4 — `listEnvelope` (`_page`, `_total_items`, `_links`,
`_embedded`); `_embedded` декодится отложенно по ключу сущности.

## amoJo Chat: подпись и контур

`amojoCall` валидирует `AmojoTarget`, затем маршалит тело **один раз** —
`Content-MD5` и реальные байты тела считаются с одних и тех же байтов (иначе MD5
разойдётся с подписью). Подпись исходящего запроса (`signRequest`) — строка из
пяти компонентов:

```
UPPER(method) \n Content-MD5 \n application/json \n Date(RFC2822) \n path
```

— и HMAC-SHA1 от неё по channel-secret. Заголовки запроса: `Date`, `Content-Type`,
`Content-MD5`, `X-Signature`. Подпись валидна 15 минут; `Date` пишется в UTC.

Верификация **входящего** вебхука — другая модель: `VerifyWebhookSignature` считает
HMAC-SHA1 по нетронутому сырому телу (без trim/re-marshal) и сравнивает в
постоянное время. amoJo шлёт голый hex без префикса `sha1=`.

`AmojoTarget.AccountKey` должен совпадать с `Account.Subdomain` того же аккаунта:
rate-budget общий для REST и amoJo (см. ниже).

## Rate-limit

Потолок amoCRM — **per-account** (по умолчанию 7 rps на аккаунт), поэтому ключ
лимитера — аккаунт (`subdomain` / `AccountKey`), не глобальный клиент. Бюджет
общий для всех outbound-вызовов аккаунта (REST + amoJo + Drive + OAuth).
`rate.Limiter` создаётся с `burst = rps` (короткий всплеск допустим, дальше
стабильные rps/сек).

Лимитер стоит перед **каждой** попыткой HTTP: первую ждёт `execute`, повторные —
хук `httpclient.WithBeforeAttempt` (`accountLimiter.beforeAttempt`, аккаунт берёт
из `ctx`). Так ретраи httpclient не превышают бюджет аккаунта.

Карта лимитеров не растёт бесконечно: аккаунт, молчавший дольше 15 минут
(`limiterIdleTTL`), удаляется при очередном обращении к лимитеру (sweep не чаще
раза в TTL). Удаление ничего не теряет — за это время bucket аккаунта полон.

Оркестрация конкурирующих потребителей бюджета в рамках одного аккаунта — у
сервиса-потребителя.

## OAuth: TTL по серверному времени

`Tokens.ExpiresAt()` считается от `ServerTime` (серверное время amoCRM из ответа) +
`ExpiresIn`, а не от локальных часов — это устраняет clock-skew между потребителем
и amoCRM. Превентивный refresh и хранение токенов — у потребителя через интерфейс
`TokenSource`; кит определяет контракт, сам не хранит и не вызывает.

`redirect_uri` обязателен в теле и для `refresh_token`-grant'а (требование wire-
контракта amoCRM), не только для `authorization_code`.

## Drive: динамический host

Drive ходит на `drive_url` аккаунта, который не выводится из subdomain — его
отдаёт `GetDriveURL` (GET `/api/v4/account?with=drive_url`). Drive-методы
принимают `driveURL` параметром и передают его в `restReq.base`, переопределяя
базовый REST-хост. Загрузка файла — сессионная и чанковая: `CreateUploadSession`
→ серия `UploadPart` (с заголовком `Content-Range`); последняя часть возвращает
финальный `File`.

## HostOverride

`Config.HostOverride` (`scheme://host`) при заданном значении подменяет **только
scheme и host** всех контуров (REST/amoJo/OAuth/Drive); путь, query, заголовки и
подпись сохраняются — `upload_url` Drive с путём и query доезжает до override-хоста
целиком. В проде пусто — хост берётся из subdomain/tld (для amoJo —
`amojo.amocrm.<tld>`). Для тестов есть и второй шов — `WithTransport`.

## Ключевые проектные решения (WHY)

- **Свой httpclient, а не общий из графа.** Хук лимитера на каждую попытку
  живёт в опциях `httpclient.New`; общий клиент приложения его не имел бы, и
  ретраи обходили бы бюджет аккаунта. Параметры (таймаут/retry/breaker) —
  `httpclient.Config` из графа, поэтому ENV `HTTPCLIENT_*` общий с приложением.
- **HTTP-коды наружу не протекают.** Потребитель различает исходы через `errors.Is`
  по sentinel'ам — это позволяет принимать решения retry/throttle/dead-
  letter, не завязываясь на конкретные числовые статусы. Структурированная деталь
  (`*APIError`) доступна через `errors.As` для логов/диагностики.
- **Координаты валидируются, а не экранируются.** Формат subdomain/tld у amoCRM
  фиксирован; всё вне его — ошибка потребителя, а не повод собирать URL «как
  получится».
- **`204` и пустое `200`-тело — не ошибка.** amoCRM (особенно RU-инстанс) отдаёт
  204 на GET без данных и пустое тело на части update/pin — кит трактует это как
  «нет данных», а не сбой.
- **Полиморфные поля — `map[string]any` / `any`.** Содержимое сообщений amoJo,
  значений custom-полей, тел react/typing строит потребитель по типу — кит передаёт
  как есть, не навязывая схему.
- **`get`-геттеры есть только у `talks`.** Остальные сущности потребитель читает из
  локального read-model (зеркала), а не read-through к amoCRM; `talks` —
  единственная сущность без надёжного webhook, поэтому её гидратируют через
  `GetTalk`.
