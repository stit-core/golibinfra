# amocrmkit — рецепты использования

Все примеры предполагают импорт:

```go
import "git.stit.tech/stit-core/golibinfra/amocrmkit"
```

Версию и `go get` — см. [корневой README](../../README.md) § Использование в проекте.

## Подключение через fx

`Module` provide'ит `*amocrmkit.Client`, забирая из графа `Config`,
`*httpclient.Client`, `clockkit.Clock` и `logkit.Logger`. `Config` регистрирует
caller — `Module` его не provide'ит (та же asymmetry, что в `pgkit`).

```go
app := fx.New(
    httpclient.Module,
    clockkit.Module,
    logkit.Module,
    amocrmkit.Module,
    fx.Provide(amocrmkit.NewConfig),
    fx.Invoke(func(c *amocrmkit.Client) {
        acc := amocrmkit.Account{Subdomain: "mycompany", TLD: "ru", Token: accessToken}
        page, err := c.ListLeads(ctx, acc, amocrmkit.ListParams{Limit: 50})
        _, _ = page, err
    }),
)
```

Минимальные ENV — нет обязательных: `Config` целиком на дефолтах
(`AMOCRM_RATE_RPS=7`, `AMOCRM_TIMEOUT=10s`, `AMOCRM_HOST_OVERRIDE=""`). Подробности —
[configuration.md](configuration.md).

## Подключение через прямой New

```go
c, err := amocrmkit.New(amocrmkit.Config{RateRPS: 7}, hc, clockkit.Real{}, logkit.Nop())
if err != nil {
    // ...
}
acc := amocrmkit.Account{Subdomain: "mycompany", TLD: "ru", Token: accessToken}
created, err := c.CreateLeads(ctx, acc, []amocrmkit.LeadInput{{Name: "Сделка"}})
```

Сигнатура конструктора:

```go
func New(cfg Config, hc *httpclient.Client, clock clockkit.Clock, log logkit.Logger) (*Client, error)
```

`hc` обязателен (`nil` → ошибка); `RateRPS` обязан быть `> 0`. При `clock == nil`
подставляется `clockkit.Real{}`, при `log == nil` — `logkit.Nop()`.

## REST v4: координаты аккаунта

Каждый REST/Drive-вызов принимает `Account` — координаты аккаунта, которые
gateway собирает из своего token-store на каждый вызов (kit их не хранит):

```go
acc := amocrmkit.Account{
    Subdomain: "mycompany", // <subdomain>.amocrm.<tld>
    TLD:       "ru",        // "ru" | "com" — домены невзаимозаменяемы
    Token:     accessToken, // OAuth access token, уходит в Authorization: Bearer
}
```

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
завязаны на `Account` (subdomain/tld/секреты приходят параметрами):

```go
tok, err := c.ExchangeCode(ctx, "mycompany", "ru", clientID, clientSecret, code, redirectURI)
// tok.AccessToken, tok.RefreshToken, tok.ExpiresIn, tok.ServerTime
expiresAt := tok.ExpiresAt() // = ServerTime + ExpiresIn (UTC), не локальные часы

tok2, err := c.RefreshToken(ctx, "mycompany", "ru", clientID, clientSecret, refreshToken, redirectURI)
```

`redirect_uri` обязателен в теле **обоих** grant'ов, включая `refresh_token`
(требование wire-контракта amoCRM).

Хранение токенов, превентивный refresh (порог 5 мин до истечения) и оркестрация —
у gateway. Kit определяет контракт `TokenSource` для gateway, но сам его не
вызывает — актуальный access-токен gateway кладёт в `Account.Token` перед вызовом:

```go
type TokenSource interface {
    Token(ctx context.Context, accountSubdomain string) (string, error)
}
```

## amoJo Chat

amoJo-вызовы принимают `AmojoTarget` вместо `Account`. `AccountKey` обязан
совпадать с `Account.Subdomain` того же аккаунта — rate-budget (7 rps) общий с
REST/Drive.

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
//    silent=true → import (тихо в историю). Тело message полиморфно — строит gateway.
msg, err := c.AmojoNewMessage(ctx, tgt, res.ScopeID, amocrmkit.NewMessagePayload{
    MsgID:   "my-msg-1",
    Sender:  amocrmkit.AmojoParticipant{ID: "client-1", Name: "Иван"},
    Message: map[string]any{"type": "text", "text": "Здравствуйте"},
})
```

Эфемерные/служебные вызовы (`AmojoReact`, `AmojoTyping`, `AmojoDeliveryStatus`)
принимают `map[string]any` — точное тело строит gateway по message-types.

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

## Drive: чанковая загрузка файла

Drive ходит на динамический `drive_url` аккаунта (не выводится из subdomain).
Сначала резолвим его, затем сессионная загрузка чанками:

```go
driveURL, err := c.GetDriveURL(ctx, acc)

sess, err := c.CreateUploadSession(ctx, acc, driveURL, amocrmkit.UploadSessionInput{
    FileName: "doc.pdf",
    FileSize: total,
})

uploadURL := sess.UploadURL
var file amocrmkit.File
offset := int64(0)
for _, chunk := range chunks { // размер чанка ≤ sess.MaxPartSize
    contentRange := fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(len(chunk))-1, total)
    file, err = c.UploadPart(ctx, acc, uploadURL, contentRange, chunk)
    // промежуточный ответ несёт next upload_url; последняя часть — финальный File
    offset += int64(len(chunk))
}

// Привязать загруженный файл к сущности (202).
err = c.AttachFiles(ctx, acc, "leads", leadID, []amocrmkit.AttachFileInput{{FileUUID: file.UUID}})
```

## Обработка ошибок

HTTP-коды наружу не протекают — потребитель различает исходы через `errors.Is` по
sentinel'ам и при необходимости достаёт деталь через `errors.As(&*APIError)`:

```go
_, err := c.ListLeads(ctx, acc, amocrmkit.ListParams{})
switch {
case err == nil:
    // ok
case errors.Is(err, amocrmkit.ErrRateLimited):
    var apiErr *amocrmkit.APIError
    if errors.As(err, &apiErr) {
        // apiErr.RetryAfter — секунды (поле тела retry_after на 429)
    }
    // throttle / отложить
case errors.Is(err, amocrmkit.ErrAuth):
    // 401 — обновить токен через token-store и повторить
case errors.Is(err, amocrmkit.ErrValidation):
    var apiErr *amocrmkit.APIError
    if errors.As(err, &apiErr) {
        // apiErr.ValidationErrors — поля с ошибками (400)
    }
case errors.Is(err, amocrmkit.ErrServer):
    // 5xx — retry/dead-letter по политике gateway
}
```

Полная таксономия sentinel'ов и маппинг кодов — [api.md](api.md) § Ошибки и
[architecture.md](architecture.md) § Ключевые проектные решения.
