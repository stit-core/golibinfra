# authnkit — устройство

`authnkit` — набор не связанных между собой примитивов аутентификации. Кит
сознательно не строит flow: нет сессий, refresh-ротации, account lockout,
enrollment'а MFA, хранилища секретов. Это «кирпичики», из которых потребитель
собирает свой auth-домен, и каждый из них криптографически корректен по
умолчанию.

```
authnkit/
├── doc.go           package-doc
├── hasher.go        Hasher: Argon2idHasher (хэш пароля, NeedsRehash, VerifyDummy)
├── jwt.go           JWTSigner/JWTParser: HS256JWT (подпись/разбор токена, go-jose)
├── totp.go          TOTPGenerator (RFC 6238, обёртка pquerna/otp) + UsedCodeStore
├── usedcode.go      MemoryUsedCodeStore (однократность кода в одной реплике)
├── token.go         NewToken / HashToken (одноразовые непрозрачные токены)
├── permissions.go   RBAC: Permission / Registry (экземплярный реестр)
├── metrics.go       OTel-инструменты (создаются в конструкторах через internal/otelkit)
└── module.go        fx.Module + envconfig-конструкторы
```

## Примитивы

### Hashing — Argon2id

`Hasher` — интерфейс из четырёх методов (`Hash`, `Verify`, `NeedsRehash`,
`VerifyDummy`). Единственная реализация — `Argon2idHasher` поверх
`golang.org/x/crypto/argon2` (`argon2.IDKey`, Argon2**id**-вариант).

- `Hash` генерирует случайную соль (`crypto/rand`, длина `SaltLen`) и кодирует
  результат в самоописывающую PHC-строку:
  `$argon2id$v=19$m=<memory>,t=<time>,p=<threads>$<salt-b64>$<hash-b64>`.
  Base64 — `RawStdEncoding` без padding, декодируется строго (`Strict`).
- `Verify` парсит эту строку, **извлекает параметры из самого хэша** (memory,
  time, threads, длину ключа), пересчитывает Argon2id и сравнивает в постоянном
  времени (`crypto/subtle.ConstantTimeCompare`).
- `NeedsRehash(encoded)` — параметры строки отличаются от параметров хэшера
  (или строка нечитаема): сигнал перехэшировать пароль после успешного логина.
- `VerifyDummy(ctx)` — тот же объём вычислений без сравнения: путь
  «пользователь не найден» отвечает столько же, сколько «пароль неверен», иначе
  время ответа перечисляет учётные записи.
- Одновременно хэшер считает не больше `max(1, GOMAXPROCS/Threads)` Argon2id —
  `Hash`, `Verify` и `VerifyDummy` делят одни слоты. Каждое вычисление держит
  `Memory` КиБ, а логин анонимен: без предела наплыв запросов множил бы память
  без выигрыша в скорости — `Threads` lanes уже занимают ядра. Лишние вызовы
  ждут слот; отмена `ctx` прерывает ожидание ошибкой с `ctx.Err()`, у
  `VerifyDummy` — пропуском вычисления.

WHY параметры зашиты в хэш: старые хэши проверяются корректно даже после того,
как `Argon2idParams` подняли (стойкость растёт со временем). Соль уникальна на
каждый `Hash` — два вызова с одним паролем дают разные строки.

**Окно параметров.** Параметры PHC-строки приходят из хранилища, а значит
задавали бы CPU и RAM одной проверки. И `Hash`, и `Verify` принимают только
`t` в `1..64`, `p` в `1..16`, `m` в `8 МиБ..1 ГиБ`, соль `8..64` байт, хэш
`16..128` байт (константы `Min*`/`Max*`) — и сверх того ограничивают
**стоимость**: `t * m` не больше `MaxCostKiBPasses` (786432 КиБ·прохода —
четыре дефолтных хэша). Каждая граница по отдельности пропускает и `t=64`, и
`m=1 ГиБ`, а вместе они стоят ~10 с и гигабайт на один `Verify`.
Проверка окна идёт **до** `argon2.IDKey`, поэтому строка с `t=2^31`
отвергается мгновенно. Строка вне окна, чужой `v=`, битый или неканоничный
base64 (`RawStdEncoding.Strict`: мусор в хвостовых битах — это другое
представление того же хэша), неканоническое число (`m=0065536`), пробел или
перевод строки, лишние или недостающие поля, иной порядок `m,t,p` →
`(false, ErrInvalidHash)`. Параметры самого хэшера вне окна → `NewArgon2idHasher`
возвращает `ErrConfig` на старте приложения.

### JWT — HS256

`HS256JWT` реализует `JWTSigner` (`Sign`) и `JWTParser` (`Parse`) поверх
`github.com/go-jose/go-jose/v4` — той же библиотеки, что `oidckit` и `hubkit`.
Алгоритм — симметричный HMAC-SHA256; секрет общий для подписи и проверки
(`JWTConfig.Secret`, не короче 32 байт — иначе `NewHS256JWT` → `ErrConfig`).

- `Sign(ctx, claims, ttl)` копирует переданные claims, проставляет `exp = now+ttl`
  и `iat = now`; `ttl <= 0` → `ErrConfig`. `SignAccess` / `SignRefresh` — то же с
  `JWTConfig.AccessTTL` / `RefreshTTL` (нулевые значения конфига → дефолты
  15m / 720h) плюс claim `typ`.
- `Parse(ctx, token)` верифицирует подпись и сроки. Алгоритм закреплён:
  принимается только `alg: HS256` (`none`, RS*, HS384/512 → `ErrTokenMalformed`).
  `exp` обязателен — токен без него `ErrTokenMalformed`. `exp`/`nbf`/`iat`
  сверяются с leeway (`JWTConfig.Leeway`, default 60s): `ErrTokenExpired` /
  `ErrTokenSignature` / `ErrTokenMalformed` — потребитель матчит их через
  `errors.Is`, не разбирая ошибки go-jose.
- `ParseAccess` / `ParseRefresh` дополнительно требуют `typ=access|refresh`
  (`ErrWrongTokenType`).
- `WithIssuer` / `WithAudience` включают симметричную проверку `iss`/`aud`:
  что проставлено при подписи, то требуется при разборе (`ErrTokenAudience`).
- Часы — `clockkit.Clock` (`WithClock`); по умолчанию `clockkit.Real{}`.

Claims на выходе — `map[string]any`. Числовые значения (`exp`, `iat`, любые
свои) приходят как `float64` — это поведение JSON-декодера `encoding/json`,
учитывай при чтении.

WHY тип токена внутри кита: `SignAccess`/`SignRefresh` различались бы только
сроком жизни, и refresh-токен проходил бы проверку access-контура. Проверку
типа нельзя оставлять потребителю — кит, который выпускает два вида токенов,
обязан их и различать.

### TOTP — RFC 6238

`TOTPGenerator` — тонкая обёртка над `github.com/pquerna/otp/totp`.
Конфиг — `TOTPConfig{Issuer, Skew}`.

- `Generate(accountName)` создаёт новый секрет и возвращает его base32-форму +
  `otpauth://`-URL (его можно отдать как QR в authenticator-приложение).
  Параметры URL заданы явно (период 30 с, 6 цифр, SHA1) — теми же, по которым
  считает `Verify`; `:` и `/` в issuer и accountName отвергаются, иначе label
  URL разбирается приложениями неоднозначно.
- `Verify(ctx, secret, code)` проверяет код в окне `±Skew` шагов (`TOTP_SKEW`,
  default 1, потолок 2 — толеранс к рассинхрону часов и задержке ввода).
  Нечитаемый секрет из хранилища — ошибка, а не «код неверен».

**Replay.** Внутри окна один и тот же код валиден несколько раз. `VerifyOnce`
закрывает это через `UsedCodeStore` (ключ — SHA-256 от секрета и кода, TTL —
длина окна с учётом `Skew`): `MemoryUsedCodeStore` для одной реплики, обёртка
над `cachekit.Cache.SetNX` — для нескольких. Голый `Verify` состояния не хранит.

### Одноразовые токены

`NewToken` — 32 байта `crypto/rand` в hex: 256 бит энтропии, 64 символа без
алфавита URL-кодирования. Hex выбран под маски logkit: цепочку из ≥ 32 hex-символов
дефолтный набор маскирует в любом значении лога, base64url — нет. `HashToken` —
SHA-256 в hex: перебор полной энтропии невозможен, поэтому Argon2id здесь только
тратил бы CPU, а детерминированный хеш даёт поиск по индексу. Хранение и
погашение — у владельца таблицы (условный `UPDATE`).

### RBAC — экземплярный реестр прав

`Registry` — простейший статический RBAC: соответствие «роль → набор прав» под
`sync.RWMutex`. Провайдится `Module` (один на приложение), в тестах создаётся
`NewRegistry()` на тест — общего process-global состояния нет.

- `Register(role, perms...)` записывает набор прав роли (полностью
  перезаписывает предыдущий для этой роли).
- `Has(role, perm)` — потокобезопасная проверка под `RLock`. Неизвестная роль
  или право → `false` (fail-closed).

WHY статично: это базовый guard для ролей, известных на старте приложения.
Динамические права на пользователя, иерархии ролей, скоупы из БД — это уже
доменная логика потребителя, кит её не навязывает.

## Observability

Инструменты создаются в конструкторах (`NewArgon2idHasher`, `NewHS256JWT`,
`NewTOTP`) через `internal/otelkit` и глобальный `otel.Meter`. Глобальный
провайдер OTel — делегирующий: инструменты, созданные до первого
`SetMeterProvider`, переезжают на этот провайдер при его регистрации, поэтому
порядок «конструктор → провайдер» не критичен. Полный справочник сигналов —
[observability.md](observability.md).

## fx и конфигурация

`Module` (`module.go`) провайдит `*Argon2idHasher` и `Hasher`, `*HS256JWT` и его
интерфейсы (`JWTSigner`, `JWTParser`), `*TOTPGenerator` и `*Registry`. `Module`
**не** провайдит сами конфиги — их регистрирует приложение (типично через
App-структуру), однородно с `httpkit`/`pgkit`. Для приложений без App-pattern
есть envconfig-конструкторы `NewArgon2idParams` / `NewJWTConfig` / `NewTOTPConfig`
(читают ENV через `kelseyhightower/envconfig` и валидируют результат).
`fx.Provide` ленив: `TOTPConfig` требуется только приложению, которое просит
`*TOTPGenerator`.

Детали ENV — [configuration.md](configuration.md); рецепты подключения —
[usage.md](usage.md).
