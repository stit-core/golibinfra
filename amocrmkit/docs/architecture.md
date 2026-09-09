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
  Bearer, загрузка файлов частями.

Кит **stateless**: не хранит токены, не ведёт persistence, не общается с
очередями, не содержит бизнес-логики и не оркестрирует мультиаккаунт. Всё это —
ответственность потребителя. Один экземпляр `Client` обслуживает все аккаунты.

## Per-account, а не per-instance

Координаты аккаунта приходят аргументами вызовов, не глобальной конфигурацией:

- REST/Drive — структура `Account{Subdomain, TLD, Token}`.
- amoJo — структура `AmojoTarget{TLD, AccountKey, Secret}`.
- OAuth — тот же `Account` (без `Token`) плюс `OAuthConfig{ClientID, ClientSecret,
  RedirectURI}`.

Выбор аккаунта и хранение токенов (token-store) — у потребителя. `Config` несёт
только то, что одинаково для всех аккаунтов.

`.ru` и `.com` — разные домены и невзаимозаменяемы; TLD выбирает потребитель по
аккаунту. Allowlist доменов — `amocrm.ru` и `amocrm.com`: Kommo (`kommo.com`,
`amojo.kommo.com`) вне scope кита, такие координаты отвергаются
`ErrInvalidAccount`.

### Валидация координат

Subdomain и TLD попадают в host запроса, а с ним уходят Bearer-токен и
`client_secret`. Поэтому каждый путь, который строит URL (REST, Drive, OAuth,
amoJo), сначала проверяет `Subdomain`/`AccountKey` по `^[a-z0-9-]{1,63}$` и
`TLD ∈ {ru, com}`; несоответствие → `ErrInvalidAccount` без сетевого вызова.
Server-provided адреса Drive (`drive_url`, `upload_url`) проверяются на
абсолютный `https`-URL, хост которого лежит на `*.amocrm.<TLD>` аккаунта: они
приходят из ответов сервера и token-store потребителя, а вместе с запросом
уезжает Bearer. При `HostOverride` проверка выключена (sandbox/e2e).

### Экранирование сегментов пути

Динамические сегменты (`uid` неразобранного, `uuid` файла, id группы полей,
`scope_id`/`msgid` amoJo) экранируются `url.PathEscape`: значение с `?`, `#` или
`/` иначе переписало бы адрес вызова. Имена сущностей в пути (`entity`,
`entity_type`) не экранируются, а проверяются по allowlist
(`leads|contacts|companies|customers`, для доп-полей — плюс `catalogs/{id}`);
отказ — `ErrInvalidRequest`, до сети. amoJo подписывает `u.EscapedPath()` — ровно
тот путь, который уйдёт на провод.

Экранирование не трогает точки, поэтому собранный путь дополнительно проверяется
целиком (`checkPath` в `resolveURL`): пустой сегмент, `.` и `..` — тоже
`ErrInvalidRequest` до сети. Иначе сервер, нормализующий путь, выполнил бы не тот
вызов, о котором просил потребитель.

## Компоненты

| Компонент | Файл | Роль |
|---|---|---|
| `Client` | `client.go` | единая точка: собирает `httpclient` с хуком лимитера, держит rate-limiter и OTel-инструменты; `Account`, валидация, `resolveURL`, `execute` |
| `accountLimiter` | `ratelimit.go` | per-account token-bucket (`golang.org/x/time/rate`) с TTL-вытеснением простаивающих аккаунтов и хуком `beforeAttempt` для ретраев |
| `instruments` | `metrics.go` | счётчики/гистограмма OTel через `internal/otelkit` |
| таксономия ошибок | `errors.go` | sentinel'ы `Err*` + `*APIError` |
| подпись amoJo | `signing.go` | исходящая 5-компонентная подпись + верификация вебхука |
| OAuth | `oauth.go` | `ExchangeCode`/`RefreshToken`, `OAuthConfig`, `TokenPair` |
| сериализация refresh | `refresh.go` | `TokenStore` + `RefreshCoordinator` (single-flight на аккаунт) |
| 429 → пауза | `ratelimit_transport.go` | поднимает `retry_after` из тела 429 в заголовок `Retry-After` и штрафует лимитер аккаунта |
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
     пауза лимитера аккаунта на `retry_after`, инкремент
     `amocrm_rate_limited_total{result="remote"}` и `Warn`-лог, для 5xx — `Warn`-лог;
   - `204` / пустое тело → для списка и батча «нет данных» (ошибки нет), для
     одиночной сущности (`getOne`, OAuth) — `ErrNotFound`;
   - иначе декод JSON в `out`; тело успешного ответа ограничено 32 MiB
     (`ErrResponseTooLarge`).

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

## Retry-политика

Кит владеет своим `httpclient`, поэтому задаёт политику повторов сам:

- `RetryNonIdempotent` у клиента кита принудительно выключен, а `GetBody`
  мутирующего POST/PATCH обнуляется — httpclient не повторяет запрос, тело
  которого нельзя перечитать. Так повтор создания сущности не проходит и через
  чужой `*httpclient.Client` (`WithHTTPClient`), где конфигурацию кит не
  контролирует. Серверной идемпотентности у amoCRM нет: `request_id`
  дедуплицирует только внутри одного батча.
- Повтор разрешается точечно, через `httpclient.AllowRetry(ctx)`: сейчас это
  ephemeral-вызовы amoJo (`typing`, `delivery_status`).
- GET/PUT/DELETE идемпотентны по методу и повторяются политикой httpclient
  (`408/429/502/503/504`).

## Rate-limit

Потолок amoCRM — **per-account** (по умолчанию 7 rps на аккаунт), поэтому ключ
лимитера — аккаунт (`subdomain` + TLD: `demo.amocrm.ru` и `demo.amocrm.com` —
разные аккаунты), не глобальный клиент. Бюджет общий для всех outbound-вызовов
аккаунта (REST + amoJo + Drive + OAuth). `rate.Limiter` создаётся с `burst = rps`
(короткий всплеск допустим, дальше стабильные rps/сек).

Лимитер стоит перед **каждой** попыткой HTTP: первую ждёт `execute`, повторные —
хук `httpclient.WithBeforeAttempt` (`accountLimiter.beforeAttempt`, аккаунт берёт
из `ctx`). Так ретраи httpclient не превышают бюджет аккаунта.

Карта лимитеров не растёт бесконечно: аккаунт, молчавший дольше 15 минут
(`limiterIdleTTL`) и не находящийся на паузе, удаляется при очередном обращении к
лимитеру (sweep не чаще раза в TTL). Удаление ничего не теряет — за это время
bucket аккаунта полон.

### 429 и пауза аккаунта

amoCRM отдаёт задержку полем тела `retry_after`, а не только заголовком.
Транспорт кита поднимает её в `Retry-After` (backoff httpclient умеет только
заголовок) и **штрафует лимитер аккаунта**: сам заголовок разбирает общий с
httpclient парсер `httpclient.ParseRetryAfter` (обе формы RFC 9110 — секунды и
HTTP-дата), пауза обрезается сутками; до конца паузы ни ретраи текущего
вызова, ни параллельные вызовы того же аккаунта в сеть не уходят. Это важно,
потому что по доке amoCRM повторное превышение rps ведёт к 403 (блокировке
аккаунта), а не к очередному 429.

Ожидание слота ограничено `Config.MaxWait` (очередь + пауза): дольше — вызов
отклоняется локально с `ErrRateLimited` и инкрементом
`amocrm_rate_limited_total{result="local"}`, не занимая горутину на всю паузу.
Решение «повторить позже» остаётся за очередью потребителя.

Оркестрация конкурирующих потребителей бюджета в рамках одного аккаунта — у
сервиса-потребителя.

## OAuth: TTL по серверному времени и одноразовый refresh

`TokenPair.ExpiresAt()` считается от `ServerTime` (серверное время amoCRM из
ответа) + `ExpiresIn`, а не от локальных часов — это устраняет clock-skew между
потребителем и amoCRM. Если `server_time` в ответе не пришёл, кит подставляет
`clock.Now()`: иначе TTL считался бы от 1970 и потребитель рефрешил бы по кругу.

`redirect_uri` обязателен в теле и для `refresh_token`-grant'а (требование wire-
контракта amoCRM), не только для `authorization_code`.

Refresh-токен amoCRM одноразовый: после обмена старый недействителен. Хранение
пары остаётся у потребителя (`TokenStore`), но сериализацию refresh на аккаунт
кит берёт на себя — `RefreshCoordinator` поверх `singleflight`: конкурентные
вызовы делят один обмен, новая пара сохраняется до возврата вызывающему, а отмена
ctx одного вызывающего не отменяет общий обмен. Между репликами гонку снимает
блокировка в `TokenStore` (`SELECT … FOR UPDATE`).

## Drive: динамический host

Drive ходит на `drive_url` аккаунта, который не выводится из subdomain — его
отдаёт `GetDriveURL` (GET `/api/v4/account?with=drive_url`). Drive-методы
принимают `driveURL` параметром и передают его в `restReq.base`, переопределяя
базовый REST-хост (с проверкой домена, см. § Валидация координат). Загрузка
файла — сессионная и по частям: `CreateUploadSession` → серия `UploadPart` (с
заголовком `Content-Range`), где промежуточный ответ несёт `next_url` следующей
части, а финальный — сам `File`. `UploadFile` проходит эту цепочку из `io.Reader`,
держа в памяти не больше одной части.

## HostOverride

`Config.HostOverride` (`scheme://host`) при заданном значении подменяет **только
scheme и host** всех контуров (REST/amoJo/OAuth/Drive); путь, query, заголовки и
подпись сохраняются — `upload_url` Drive с путём и query доезжает до override-хоста
целиком. В проде пусто — хост берётся из subdomain/tld (для amoJo —
`amojo.amocrm.<tld>`). Для тестов есть и второй шов — `WithTransport`.

## Ключевые проектные решения (WHY)

- **Свой httpclient, а не общий из графа.** Хук лимитера и транспорт, который
  видит 429, живут в опциях `httpclient.New`; общий клиент приложения их не имел
  бы, и ретраи обходили бы бюджет аккаунта. Параметры (таймаут/retry/breaker) —
  `httpclient.Config` из графа; отдельная политика для amoCRM собирается через
  `httpclient.NewConfigPrefixed("AMOCRM")`.
- **HTTP-коды наружу не протекают.** Потребитель различает исходы через `errors.Is`
  по sentinel'ам — это позволяет принимать решения retry/throttle/dead-
  letter, не завязываясь на конкретные числовые статусы. Структурированная деталь
  (`*APIError`) доступна через `errors.As` для логов/диагностики.
- **Координаты валидируются, сегменты пути экранируются.** Формат subdomain/tld у
  amoCRM фиксирован — всё вне его ошибка потребителя; а `uid`/`uuid`/`scope_id`
  приходят из внешних систем, и их безопасно экранировать, а не отвергать.
- **Отказ до сети — отдельный класс ошибок.** `ErrInvalidRequest`/
  `ErrInvalidAccount` перманентны по построению: потребителю не нужно гадать,
  transient это или нет.
- **`204` и пустое `200`-тело — не ошибка.** amoCRM (особенно RU-инстанс) отдаёт
  204 на GET без данных и пустое тело на части update/pin — кит трактует это как
  «нет данных», а не сбой.
- **Полиморфные поля — `map[string]any` / `any`.** Содержимое сообщений amoJo,
  значений custom-полей, тел react/typing строит потребитель по типу — кит передаёт
  как есть, не навязывая схему.
- **Пустой ответ на одиночную сущность — `ErrNotFound`.** Нулевой объект с
  `err == nil` неотличим от найденного; для списков и батчей пустой ответ
  по-прежнему «нет данных».
- **Wire-контракты сверены с докой amoCRM.** Матрица связей асимметрична
  (`companies→leads` допустима, `contacts→leads` — нет); активация пользователей —
  batch `POST /api/v4/users/{activate,deactivate}` с телом `[{"id":…}]` и без тела
  ответа; CRM Plugin живёт на `/api/v4/website_buttons` (создание асинхронное:
  ответ несёт только `source_id` и `trusted_websites`).
