# webpushkit

Транспорт доставки Web Push: VAPID-аутентификация (RFC 8292), aes128gcm-шифрование
payload (RFC 8291 поверх RFC 8188) и POST на endpoint push-сервиса (RFC 8030).
Kit — только типы и отправка; хранение подписок и планирование уведомлений — зона
потребителя (notification-домен).

Сценарий: SPA-вкладка закрыта, но пользователю нужно доставить пуш (смена фазы
помодоро, дедлайн). Браузер держит подписку в своём push-сервисе; сервис POST'ит
ей зашифрованное сообщение, service worker будит и показывает нотификацию.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/webpushkit"
```

Версия модуля — см. [`../README.md`](../README.md) (§ Версионирование).

## Генерация VAPID-ключей

VAPID-пара (P-256/ES256) выдаётся один раз на сервис. Public-ключ также кладётся
в браузерный `PushManager.subscribe({ applicationServerKey })`.

```go
pub, priv, err := webpushkit.GenerateVAPIDKeys()
// pub  → WEBPUSH_VAPID_PUBLIC_KEY  (+ applicationServerKey фронта)
// priv → WEBPUSH_VAPID_PRIVATE_KEY (секрет; в Vault)
```

Оба значения — base64url без паддинга: public — uncompressed point (65 байт),
private — raw scalar (32 байта).

## Быстрый старт (fx)

`Module` provide'ит `*Sender`. Config не provide'ится (как в `cachekit`/`oidckit`) —
caller добавляет `webpushkit.NewConfig`. `*http.Client` опционален: без него —
`http.DefaultClient`; передашь свой (`httpclient`/otelhttp) — Sender возьмёт его
и вы получите транспортные RED-метрики.

```go
fx.New(
    fx.Provide(webpushkit.NewConfig), // WEBPUSH_*
    webpushkit.Module,                // *Sender
)
```

Без fx:

```go
cfg, _ := webpushkit.NewConfig()
sender, err := webpushkit.NewSender(cfg, nil) // nil → http.DefaultClient
```

## Отправка

Подписку (`endpoint` + `keys.p256dh` + `keys.auth`) потребитель хранит у себя и
передаёт в `Send` как есть:

```go
err := sender.Send(ctx, webpushkit.Subscription{
    Endpoint: sub.Endpoint,
    P256dh:   sub.P256dh,
    Auth:     sub.Auth,
}, payload,
    webpushkit.WithTTL(10*time.Minute),
    webpushkit.WithUrgency(webpushkit.UrgencyHigh),
    webpushkit.WithTopic("pomodoro"), // новый пуш с тем же топиком вытесняет прежний недоставленный
)

switch {
case err == nil:
    // доставлено push-сервису
case errors.Is(err, webpushkit.ErrSubscriptionGone):
    store.Delete(ctx, sub.ID) // 404/410: подписка мертва — удаляем
case errors.Is(err, webpushkit.ErrPayloadTooLarge):
    // payload > ~3993 байт plaintext
default:
    var pe *webpushkit.PushError // прочий не-2xx: pe.StatusCode / pe.RetryAfter (напр. 429)
    _ = errors.As(err, &pe)
}
```

`payload` — произвольные байты (обычно JSON, который читает service worker).
Потолок plaintext — ~3993 байта: столько, сколько влезает в гарантированные
push-сервисами 4096 байт после aes128gcm-накладных.

## Конфигурация

Имена env финализируются при первом потребителе (envconfig-теги — часть API kit'а).

| Env | Поле | Default | Назначение |
|---|---|---|---|
| `WEBPUSH_VAPID_PUBLIC_KEY` | `Config.VAPIDPublicKey` | — | VAPID public (uncompressed P-256, base64url); опционален — при наличии сверяется с private |
| `WEBPUSH_VAPID_PRIVATE_KEY` | `Config.VAPIDPrivateKey` | — | VAPID private (raw ES256 scalar, base64url); секрет (Vault) |
| `WEBPUSH_SUBJECT` | `Config.Subject` | — | `sub` VAPID-JWT: `mailto:` или `https://` контакт отправителя |
| `WEBPUSH_DEFAULT_TTL` | `Config.DefaultTTL` | `24h` | TTL по умолчанию (переопределяется `WithTTL`) |

## API

- `GenerateVAPIDKeys() (publicB64, privateB64 string, err error)`.
- `NewConfig() (Config, error)`; `Config{VAPIDPublicKey, VAPIDPrivateKey, Subject, DefaultTTL}`.
- `NewSender(cfg Config, client *http.Client) (*Sender, error)` (client nil → default);
  `(*Sender).Send(ctx, Subscription, payload []byte, ...SendOption) error`.
- `Subscription{Endpoint, P256dh, Auth}` — как отдаёт браузерный PushManager.
- `SendOption`: `WithTTL(time.Duration)`, `WithUrgency(Urgency)`, `WithTopic(string)`;
  `Urgency` ∈ {`UrgencyVeryLow`, `UrgencyLow`, `UrgencyNormal`, `UrgencyHigh`}.
- Sentinel'ы: `ErrConfig`, `ErrSubscription`, `ErrSubscriptionGone`,
  `ErrPayloadTooLarge`, `ErrPush`; `*PushError{StatusCode, RetryAfter, Body}` (Is `ErrPush`).
- `Module` (fx) — provide `*Sender`.

## Семантика ErrSubscriptionGone

Push-сервис ответил 404 или 410 → подписка недействительна (пользователь отозвал
разрешение, сменил устройство, чистка на стороне сервиса). Потребитель обязан
удалить такую подписку из своего хранилища и больше на неё не слать — иначе
накопятся мёртвые endpoint'ы. Прочие не-2xx (`*PushError`) — временные:
решение про retry/backoff (в т.ч. по `RetryAfter` при 429) за потребителем.

## Хранение подписок — у потребителя

Kit намеренно не хранит подписки: их модель, привязка к пользователю/устройству,
TTL и чистка мёртвых — доменная логика (notification-домен). Kit получает
`Subscription` на вход и возвращает результат доставки.

## Observability

Собственных метрик kit не эмитит: HTTP-сигналы (RED, latency) даёт `*http.Client`,
если передать обёртку `httpclient`/otelhttp в `NewSender`. Ошибки доставки
возвращаются вызывающему для доменного учёта (метрика доставленных/умерших пушей —
на стороне потребителя).

## Инварианты

- Эфемерная ECDH-пара сервера приложения и record salt — новые на каждое
  сообщение (RFC 8291): переиспользование компрометирует шифрование.
- aes128gcm кладёт `dh`/`salt` в тело; в заголовках — только
  `Content-Encoding: aes128gcm`, `Authorization: vapid t=…, k=…`, `TTL`
  (+ опц. `Urgency`/`Topic`). Legacy-схема `aesgcm` с `Crypto-Key`/`Encryption`
  не поддерживается.
- VAPID public, если задан в конфиге, обязан соответствовать private —
  иначе `ErrConfig` (k= в Authorization и applicationServerKey браузера должны
  быть одним ключом).
- `Subject` — только `mailto:`/`https:` (требование RFC 8292 к `sub`).
