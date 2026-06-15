# amocrmkit — архитектура

## Назначение и место в стеке

`amocrmkit` — транспортный слой между gateway-сервисом stit и amoCRM. Он
инкапсулирует четыре протокольных контура amoCRM и не более того:

- **REST API v4** — `https://<subdomain>.amocrm.<tld>/api/v4/...`, аутентификация
  Bearer-токеном.
- **amoJo Chat API** — `https://amojo.amocrm.<tld>/v2/origin/custom/...`,
  аутентификация подписью HMAC-SHA1 по каждому запросу.
- **OAuth2** — `https://<subdomain>.amocrm.<tld>/oauth2/access_token`, обмен code и
  refresh токенов.
- **Drive** — динамический `drive_url` аккаунта (резолвится через `GetDriveURL`),
  Bearer, чанковая загрузка файлов.

Kit **stateless**: не хранит токены, не ведёт persistence, не общается с NATS /
outbox, не содержит бизнес-логики и не оркестрирует мультиаккаунт. Всё это —
ответственность gateway-сервиса. Один экземпляр `Client` обслуживает все аккаунты.

## Per-account, а не per-instance

Координаты аккаунта приходят аргументами вызовов, не глобальной конфигурацией:

- REST/Drive — структура `Account{Subdomain, TLD, Token}`.
- amoJo — структура `AmojoTarget{TLD, AccountKey, Secret}`.
- OAuth — subdomain/tld/client_id/client_secret/code/refresh передаются отдельными
  параметрами методов.

Выбор аккаунта и хранение токенов (token-store) — у gateway. `Config` несёт только
то, что одинаково для всех аккаунтов.

`.ru` и `.com` — разные домены и невзаимозаменяемы; TLD выбирает gateway по аккаунту.

## Компоненты

| Компонент | Файл | Роль |
|---|---|---|
| `Client` | `client.go` | единая точка: собирает зависимости, держит rate-limiter и OTel-инструменты |
| `accountLimiter` | `ratelimit.go` | per-account token-bucket (`golang.org/x/time/rate`) |
| `instruments` | `metrics.go` | счётчики/гистограмма OTel |
| таксономия ошибок | `errors.go` | sentinel'ы `Err*` + `*APIError` |
| подпись amoJo | `signing.go` | исходящая 5-компонентная подпись + верификация вебхука |
| OAuth | `oauth.go` | `ExchangeCode`/`RefreshToken`, `Tokens`, `TokenSource` |
| amoJo Chat | `amojo.go` | channel lifecycle, chats, messages, react/typing/delivery |
| REST-домены | `client_*.go` | leads/contacts/companies/customers/catalogs/tasks/notes/talks/chat/files/config/account |
| shared wire-типы | `types.go` | `CustomFieldValue`, `Tag`/`TagRef`, `CreatedEntity`, `ContactRef`/`CompanyRef` |

## Поток одного REST-вызова

Все REST/Drive-методы сводятся к нескольким generic-обёрткам, которые строят
`restReq` и зовут `doREST` → `execute`:

1. `doREST` собирает URL (через `resolveURL`, учитывая `HostOverride`), маршалит
   тело в JSON, ставит заголовки (`Authorization: Bearer`, `Accept`, при теле —
   `Content-Type`).
2. `execute` — общий путь для всех контуров:
   - `accountLimiter.wait(ctx, account)` — блокирует до свободного слота либо до
     отмены ctx;
   - `httpclient.Do` (retry + circuit-breaker + клиентский OTel-span);
   - запись метрик `amocrm_api_calls_total` и `amocrm_api_duration_seconds`;
   - при `>= 400` — `classify(status, parseAPIError(resp))`; для 429 дополнительно
     инкремент `amocrm_rate_limited_total`;
   - `204` / пустое тело → `out` не трогается, ошибки нет;
   - иначе декод JSON в `out`.

### Generic-обёртки (`client.go`)

| Обёртка | Назначение |
|---|---|
| `listEntities[T]` | GET list, достаёт `_embedded[key]` как `[]T`, возвращает `Page[T]` |
| `getOne[T]` | GET одиночного объекта (`/{id}`, `/account`) |
| `sendBatch[In,Out]` | POST/PATCH массива, достаёт `_embedded[key]` как `[]Out` |
| `sendOne[Out]` | POST/PATCH одиночного объекта, декодит ответ в `Out` |
| `sendNoContent` | вызовы без полезного тела ответа (delete/detach/accept/202/204) |
| `embeddedPage[T]` | строит `Page[T]` из уже декодированного конверта (Drive-методы) |

`Page[T]` несёт `Page`, `TotalItems`, `Links` (HAL `_links`) и `Items`. Конверт
list/batch-ответов REST v4 — `listEnvelope` (`_page`, `_total_items`, `_links`,
`_embedded`); `_embedded` декодится отложенно по ключу сущности.

## amoJo Chat: подпись и контур

`amojoCall` маршалит тело **один раз** — `Content-MD5` и реальные байты тела
считаются с одних и тех же байтов (иначе MD5 разойдётся с подписью). Подпись
исходящего запроса (`signRequest`) — строка из пяти компонентов:

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
общий для всех outbound-вызовов аккаунта (REST + amoJo + Drive). `rate.Limiter`
создаётся с `burst = rps` (короткий всплеск допустим, дальше стабильные rps/сек).
Оркестрация (poller vs команды в рамках одного аккаунта) — у gateway.

## OAuth: TTL по серверному времени

`Tokens.ExpiresAt()` считается от `ServerTime` (серверное время amoCRM из ответа) +
`ExpiresIn`, а не от локальных часов — это устраняет clock-skew между gateway и
amoCRM. Превентивный refresh и хранение токенов — у gateway через интерфейс
`TokenSource`; kit его только потребляет (точнее — определяет контракт), сам не
хранит.

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

`httpclient` не принимает кастомный transport, поэтому единственный способ
дотянуться до fake/sandbox/proxy-сервера — редирект хоста. `Config.HostOverride`
(scheme://host) при заданном значении подменяет базовый хост всех контуров
(REST/amoJo/OAuth/Drive); путь, заголовки и подпись сохраняются. В проде пусто —
хост берётся из subdomain/tld (для amoJo — `amojo.amocrm.<tld>`).

## Ключевые проектные решения (WHY)

- **HTTP-коды наружу не протекают.** Потребитель различает исходы через `errors.Is`
  по sentinel'ам — это позволяет gateway принимать решения retry/throttle/dead-
  letter, не завязываясь на конкретные числовые статусы. Структурированная деталь
  (`*APIError`) доступна через `errors.As` для логов/диагностики.
- **`204` и пустое `200`-тело — не ошибка.** amoCRM (особенно RU-инстанс) отдаёт
  204 на GET без данных и пустое тело на части update/pin — kit трактует это как
  «нет данных», а не сбой.
- **Полиморфные поля — `map[string]any` / `any`.** Содержимое сообщений amoJo,
  значений custom-полей, тел react/typing строит gateway по типу — kit передаёт
  как есть, не навязывая схему.
- **`get`-геттеры есть только у `talks`.** Остальные сущности gateway отдаёт из
  локального read-model (зеркала), а не read-through к amoCRM; `talks` —
  единственная сущность без надёжного webhook, поэтому её гидратируют через
  `GetTalk`.
