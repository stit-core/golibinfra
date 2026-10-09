# amocrmkit — рецепты использования

Все примеры предполагают импорт:

```go
import "git.stit.tech/stit-core/golibinfra/amocrmkit"
```

Версию и `go get` — см. [корневой README](../../README.md) § Использование в проекте.

## Подключение через fx

`Module` provide'ит `*amocrmkit.Client`, забирая из графа `Config`,
`httpclient.Config`, `clockkit.Clock` и `logkit.Logger`. Обе конфигурации
регистрирует caller — `Module` их не provide'ит (та же asymmetry, что в
`pgkit`). Общий `*httpclient.Client` приложения кит не использует — собирает
свой, с лимитером аккаунта на каждую попытку.

```go
app := fx.New(
    clockkit.Module,
    logkit.Module,
    amocrmkit.Module,
    fx.Provide(logkit.NewConfig, httpclient.NewConfig, amocrmkit.NewConfig),
    fx.Invoke(func(c *amocrmkit.Client) {
        acc := amocrmkit.Account{Subdomain: "mycompany", TLD: "ru", Token: accessToken}
        page, err := c.ListLeads(ctx, acc, amocrmkit.ListParams{Limit: 50})
        _, _ = page, err
    }),
)
```

`logkit.Module` требует `logkit.Config` в графе, поэтому `logkit.NewConfig`
регистрируется рядом с остальными.

Минимальные ENV — нет обязательных: `Config` целиком на дефолтах
(`AMOCRM_RATE_RPS=7`, `AMOCRM_MAX_WAIT=5s`, `AMOCRM_HOST_OVERRIDE=""`),
таймаут/retry/breaker — из `HTTPCLIENT_*`. Подробности —
[configuration.md](configuration.md).

## Подключение через прямой New

```go
hcCfg, err := httpclient.NewConfig() // дефолты тегов: литерал httpclient.Config с нулями — ErrConfig
if err != nil {
    // ...
}
hcCfg.Timeout = 10 * time.Second
c, err := amocrmkit.New(amocrmkit.Config{RateRPS: 7}, hcCfg, clockkit.Real{}, logkit.Nop())
if err != nil {
    // ...
}
acc := amocrmkit.Account{Subdomain: "mycompany", TLD: "ru", Token: accessToken}
created, err := c.CreateLeads(ctx, acc, []amocrmkit.LeadInput{{Name: "Сделка"}})
```

Сигнатура конструктора:

```go
func New(cfg Config, hcCfg httpclient.Config, clock clockkit.Clock, log logkit.Logger, opts ...Option) (*Client, error)
```

`RateRPS` обязан быть `> 0` (`ErrConfig`); нулевой `MaxWait` заменяется дефолтом
(5s). `hcCfg` тоже валидируется — клиента без таймаута собрать нельзя, а
`RetryNonIdempotent` кит принудительно выключает (повтор POST в amoCRM создаёт
дубль). При `clock == nil` подставляется `clockkit.Real{}`, при `log == nil` —
`logkit.Nop()`. Опции `WithTransport` / `WithHTTPClient` — в
[configuration.md](configuration.md).

## REST v4: координаты аккаунта

Каждый REST/Drive-вызов принимает `Account` — координаты аккаунта, которые
потребитель собирает из своего token-store на каждый вызов (кит их не хранит):

```go
acc := amocrmkit.Account{
    Subdomain: "mycompany", // <subdomain>.amocrm.<tld>; ^[a-z0-9-]{1,63}$
    TLD:       "ru",        // "ru" | "com" — домены невзаимозаменяемы
    Token:     accessToken, // OAuth access token, уходит в Authorization: Bearer
}
```

Subdomain/TLD вне формата → `ErrInvalidAccount`, запрос не отправляется.

### List с пагинацией и фильтрами

```go
page, err := c.ListContacts(ctx, acc, amocrmkit.ListParams{
    Page:   2,
    Limit:  250,                                  // потолок amoCRM на страницу
    With:   []string{"leads"},                    // with=leads
    Query:  "ivan",                               // полнотекст
    Filter: map[string]string{"id": "1,2,3"},     // filter[id]=1,2,3
    Order:  map[string]string{"updated_at": "desc"},
})
// page.Page, page.TotalItems, page.Links (HAL _links: Self/Next/Prev/First/Last),
// page.Items []Contact.
```

### Batch create / update

`Create*`/`Update*` принимают срез (batch до 250 у большинства сущностей; у
шаблонов/воронок/тегов/источников — меньше, см. комментарии методов) и возвращают
`[]CreatedEntity` (id + request_id):

```go
out, err := c.CreateLeads(ctx, acc, []amocrmkit.LeadInput{
    {Name: "Сделка", Price: ptr(46333), StatusID: 142},
})
// out[i].ID, out[i].RequestID
```

`RequestID` в `*Input` — клиентский ключ дедупликации внутри батча: amoCRM
возвращает его обратно в соответствующем `CreatedEntity`.

### Чтение одной сущности

```go
lead, err := c.GetLead(ctx, acc, leadID, "contacts", "companies") // GET /leads/{id}?with=contacts,companies
contact, err := c.GetContact(ctx, acc, contactID)
company, err := c.GetCompany(ctx, acc, companyID)
customer, err := c.GetCustomer(ctx, acc, customerID)
```

Пустой ответ на такой запрос — `ErrNotFound` (нулевую сущность с `err == nil`
кит не отдаёт). Все id — `int64`.

### Завершение задачи

```go
task, err := c.CompleteTask(ctx, acc, taskID, "Дозвонился, договорились")
```

### Account-meta

```go
amojoID, err := c.GetAmojoID(ctx, acc)        // GET /account?with=amojo_id
driveURL, err := c.GetDriveURL(ctx, acc)      // GET /account?with=drive_url
types, err := c.GetAccountTaskTypes(ctx, acc) // GET /account?with=task_types
```

## OAuth2

Обмен authorization code на пару токенов и refresh — отдельные методы; они не
завязаны на `Account` (subdomain/tld/секреты приходят параметрами и проходят ту
же валидацию):

```go
app := amocrmkit.OAuthConfig{ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI}
acc := amocrmkit.Account{Subdomain: "mycompany", TLD: "ru"} // Token не нужен

pair, err := c.ExchangeCode(ctx, acc, app, code)
// pair.AccessToken, pair.RefreshToken, pair.ExpiresIn, pair.ServerTime
expiresAt := pair.ExpiresAt() // = ServerTime + ExpiresIn (UTC), не локальные часы

pair2, err := c.RefreshToken(ctx, acc, app, pair.RefreshToken)
```

`redirect_uri` обязателен в теле **обоих** grant'ов, включая `refresh_token`
(требование wire-контракта amoCRM). Если amoCRM не прислал `server_time`, кит
подставляет свои часы: `ExpiresAt` не уезжает в 1970.

### Одноразовый refresh-токен

**Refresh-токен amoCRM одноразовый.** После успешного обмена старый токен
недействителен, а новая пара существует только в ответе. Отсюда два инварианта:

1. Новую пару сохраняют **до** того, как ей воспользуются; неудача сохранения =
   потеря интеграции.
2. Refresh на аккаунт сериализуется. Два параллельных обмена (job + HTTP-хендлер,
   две реплики) — второй получает `ErrAuth`, и если первая пара не сохранилась,
   аккаунт нужно переавторизовывать вручную.

Кит даёт `RefreshCoordinator` поверх `TokenStore` потребителя — single-flight на
аккаунт: конкурентные вызовы делят один сетевой обмен.

```go
type TokenStore interface {
    Load(ctx context.Context, acc amocrmkit.Account) (amocrmkit.TokenPair, error)
    Save(ctx context.Context, acc amocrmkit.Account, p amocrmkit.TokenPair) error
}

rc := amocrmkit.NewRefreshCoordinator(c, store, app) // опция: WithRefreshSkew(10*time.Minute)

token, err := rc.Token(ctx, acc) // отдаёт живой access-токен или обновляет пару
if errors.Is(err, amocrmkit.ErrAuth) {
    // пара потеряна: аккаунту нужна переавторизация владельцем (needs_reauth)
}
acc.Token = token
page, err := c.ListLeads(ctx, acc, amocrmkit.ListParams{Limit: 50})
```

Координатор сериализует refresh **внутри процесса**. В нескольких репликах
`TokenStore.Load`/`Save` должны опираться на блокировку хранилища
(`SELECT … FOR UPDATE`), иначе гонка возвращается на уровень БД.

## amoJo Chat

amoJo-вызовы принимают `AmojoTarget` вместо `Account`. `AccountKey` обязан
совпадать с `Account.Subdomain` того же аккаунта — rate-budget общий с
REST/Drive; `TLD`/`AccountKey` валидируются как у `Account`.

```go
tgt := amocrmkit.AmojoTarget{
    TLD:        "ru",
    AccountKey: "mycompany",   // = Account.Subdomain — общий ключ rate-limiter
    Secret:     channelSecret, // channel secret для HMAC-SHA1 подписи
}

// 1. Подключить канал (channel_id выдан amoCRM при регистрации интеграции).
res, err := c.AmojoConnect(ctx, tgt, channelID, amocrmkit.ConnectInput{
    AccountID: amojoID, // amojo_id аккаунта (из GetAmojoID)
    Title:     "Мой канал",
}) // res.ScopeID = "{channel_id}_{amojo_id}" — используется во всех последующих вызовах

// 2. Создать чат.
chat, err := c.AmojoCreateChat(ctx, tgt, res.ScopeID, amocrmkit.CreateChatInput{
    ConversationID: "ext-conv-1",
    User:           amocrmkit.AmojoParticipant{ID: "client-1", Name: "Иван"},
})

// 3. Отправить сообщение. silent=false → send (создаёт неразобранное);
//    silent=true → import (тихо в историю). Тело message полиморфно — строит потребитель.
msg, err := c.AmojoNewMessage(ctx, tgt, res.ScopeID, amocrmkit.NewMessagePayload{
    MsgID:   "my-msg-1",
    Sender:  amocrmkit.AmojoParticipant{ID: "client-1", Name: "Иван"},
    Message: map[string]any{"type": "text", "text": "Здравствуйте"},
})
```

Эфемерные/служебные вызовы (`AmojoReact`, `AmojoTyping`, `AmojoDeliveryStatus`)
принимают `map[string]any` — точное тело строит потребитель по message-types.

### Верификация входящего вебхука

`VerifyWebhookSignature` проверяет подпись входящего amoJo-вебхука по **другой**
модели, чем исходящие запросы: HMAC-SHA1 по нетронутому сырому телу, сравнение в
постоянное время. amoJo шлёт голый hex без префикса `sha1=`.

```go
func handle(w http.ResponseWriter, r *http.Request) {
    raw, _ := io.ReadAll(r.Body) // НЕ re-marshal: подпись считается по точным байтам
    sig := r.Header.Get("X-Signature")
    if !amocrmkit.VerifyWebhookSignature(raw, sig, channelSecret) {
        w.WriteHeader(http.StatusUnauthorized)
        return
    }
    // ... разобрать raw
}
```

## Drive: загрузка файла

Drive ходит на динамический `drive_url` аккаунта (не выводится из subdomain).
Сначала резолвим его, затем грузим файл — `UploadFile` сам режет поток на части
по `max_part_size` и идёт по цепочке `next_url`:

```go
driveURL, err := c.GetDriveURL(ctx, acc)

f, err := os.Open("doc.pdf")
defer f.Close()
st, err := f.Stat()

file, err := c.UploadFile(ctx, acc, driveURL, amocrmkit.UploadSessionInput{
    FileName:    "doc.pdf",
    FileSize:    st.Size(), // обязан совпадать с длиной потока
    ContentType: "application/pdf",
}, f)

// Привязать загруженный файл к сущности (202).
err = c.AttachFiles(ctx, acc, "leads", leadID, []amocrmkit.AttachFileInput{{FileUUID: file.UUID}})
```

В памяти живёт не больше одной части. Ручной цикл нужен, только если частями
управляет сам потребитель (докачка, свой источник данных):

```go
sess, err := c.CreateUploadSession(ctx, acc, driveURL, in)

uploadURL, offset := sess.UploadURL, int64(0)
for _, chunk := range chunks { // размер чанка ≤ sess.MaxPartSize
    contentRange := fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(len(chunk))-1, in.FileSize)
    part, err := c.UploadPart(ctx, acc, uploadURL, contentRange, chunk)
    if part.NextURL == "" {
        file = part // последняя часть — финальный File с UUID
        break
    }
    uploadURL = part.NextURL // промежуточный ответ: адрес следующей части
    offset += int64(len(chunk))
}
```

`driveURL` и `upload_url` должны лежать на `https://*.amocrm.<TLD>` аккаунта,
иначе кит откажет с `ErrInvalidAccount`: Bearer не уходит на чужой хост.

## Обработка ошибок

HTTP-коды наружу не протекают — потребитель различает исходы через `errors.Is` по
sentinel'ам и при необходимости достаёт деталь через `errors.As(&*APIError)`:

```go
_, err := c.ListLeads(ctx, acc, amocrmkit.ListParams{})
switch {
case err == nil:
    // ok
case errors.Is(err, amocrmkit.ErrInvalidAccount), errors.Is(err, amocrmkit.ErrInvalidRequest):
    // отказ до сети (координаты, сегмент пути, связь вне матрицы) — retry бесполезен
case errors.Is(err, amocrmkit.ErrRateLimited):
    var apiErr *amocrmkit.APIError
    if errors.As(err, &apiErr) {
        // apiErr.RetryAfter — секунды (поле тела retry_after или заголовок Retry-After).
        // Ошибка без APIError = лимитер отклонил вызов локально (аккаунт на паузе).
    }
    // отложить аккаунт, не повторять сразу
case errors.Is(err, amocrmkit.ErrPaymentRequired):
    // 402 — подписка закончилась: пауза аккаунта до оплаты, не dead-letter
case errors.Is(err, amocrmkit.ErrForbidden):
    // 403 — нет прав ЛИБО аккаунт заблокирован за превышение rps: пауза, не dead-letter
case errors.Is(err, amocrmkit.ErrAuth):
    // 401 — обновить пару через RefreshCoordinator и повторить
case errors.Is(err, amocrmkit.ErrValidation):
    var apiErr *amocrmkit.APIError
    if errors.As(err, &apiErr) {
        // apiErr.ValidationErrors — поля с ошибками (400)
    }
case errors.Is(err, amocrmkit.ErrServer):
    // 5xx — retry/dead-letter по политике потребителя
}
```

Полная таксономия sentinel'ов и маппинг кодов — [api.md](api.md) § Ошибки и
[architecture.md](architecture.md) § Ключевые проектные решения.
