# authnkit — устройство

`authnkit` — набор не связанных между собой примитивов аутентификации. Кит
сознательно не строит flow: нет сессий, refresh-ротации, account lockout,
enrollment'а MFA, хранилища секретов. Это «кирпичики», из которых потребитель
собирает свой auth-домен, и каждый из них криптографически корректен по
умолчанию.

```
authnkit/
├── hasher.go        Hasher: Argon2idHasher (хэш пароля, NeedsRehash)
├── jwt.go           JWTSigner/JWTParser: HS256JWT (подпись/разбор токена, go-jose)
├── totp.go          TOTPGenerator (RFC 6238, обёртка pquerna/otp)
├── permissions.go   RBAC: Permission / Registry (экземплярный реестр)
├── metrics.go       OTel-инструменты (создаются в конструкторах через internal/otelkit)
└── module.go        fx.Module + envconfig-конструкторы
```

## Четыре примитива

### Hashing — Argon2id

`Hasher` — интерфейс из двух методов (`Hash`, `Verify`). Единственная реализация
— `Argon2idHasher` поверх `golang.org/x/crypto/argon2` (`argon2.IDKey`,
Argon2**id**-вариант).

- `Hash` генерирует случайную соль (`crypto/rand`, длина `SaltLen`) и кодирует
  результат в самоописывающую PHC-строку:
  `$argon2id$v=19$m=<memory>,t=<time>,p=<threads>$<salt-b64>$<hash-b64>`.
  Base64 — `RawStdEncoding` (без padding).
- `Verify` парсит эту строку, **извлекает параметры из самого хэша** (memory,
  time, threads, длину ключа), пересчитывает Argon2id и сравнивает в постоянном
  времени (`crypto/subtle.ConstantTimeCompare`).
- `NeedsRehash(encoded)` — параметры строки отличаются от параметров хэшера
  (или строка нечитаема): сигнал перехэшировать пароль после успешного логина.

WHY параметры зашиты в хэш: старые хэши проверяются корректно даже после того,
как `Argon2idParams` подняли (стойкость растёт со временем). Соль уникальна на
каждый `Hash` — два вызова с одним паролем дают разные строки.

**Окно параметров.** Параметры PHC-строки приходят из хранилища, а значит
управляют CPU и RAM процесса. И `Hash`, и `Verify` принимают только
`t ≥ 1`, `p ≥ 1`, `m` в `8 МиБ..1 ГиБ`, соль `8..64` байт, хэш `16..128` байт
(константы `Min*`/`Max*`). Строка вне окна, чужой `v=`, битый base64, лишние или
недостающие поля → `(false, ErrInvalidHash)`, не паника внутри argon2 и не
аллокация гигабайт. Параметры самого хэшера вне окна → `Hash` возвращает
`ErrConfig`.

### JWT — HS256

`HS256JWT` реализует `JWTSigner` (`Sign`) и `JWTParser` (`Parse`) поверх
`github.com/go-jose/go-jose/v4` — той же библиотеки, что `oidckit` и `hubkit`.
Алгоритм — симметричный HMAC-SHA256; секрет общий для подписи и проверки
(`JWTConfig.Secret`, не короче 32 байт — иначе `NewHS256JWT` → `ErrConfig`).

- `Sign(claims, ttl)` копирует переданные claims, проставляет `exp = now+ttl` и
  `iat = now`. `SignAccess` / `SignRefresh` — то же с `JWTConfig.AccessTTL` /
  `RefreshTTL` (нулевые значения конфига → дефолты 15m / 720h).
- `Parse(token)` верифицирует подпись и сроки. Алгоритм закреплён: принимается
  только `alg: HS256` (`none`, RS*, HS384/512 → `ErrTokenMalformed`). `exp`
  обязателен — токен без него `ErrTokenMalformed`. `exp`/`nbf`/`iat` сверяются с
  leeway (`JWTConfig.Leeway`, default 60s): `ErrTokenExpired` /
  `ErrTokenSignature` / `ErrTokenMalformed` — потребитель матчит их через
  `errors.Is`, не разбирая ошибки go-jose.
- Часы — `clockkit.Clock` (`WithClock`); по умолчанию `clockkit.Real{}`.

Claims на выходе — `map[string]any`. Числовые значения (`exp`, `iat`, любые
свои) приходят как `float64` — это поведение JSON-декодера `encoding/json`,
учитывай при чтении.

### TOTP — RFC 6238

`TOTPGenerator` — тонкая обёртка над `github.com/pquerna/otp/totp`.
Конфиг — `TOTPConfig{Issuer, Skew}`.

- `Generate(accountName)` создаёт новый секрет и возвращает его base32-форму +
  `otpauth://`-URL (его можно отдать как QR в authenticator-приложение).
- `Verify(secret, code)` проверяет код: период 30 с, 6 цифр, SHA1, окно
  `±Skew` шагов (`TOTP_SKEW`, default 1 — принимаются текущий, предыдущий и
  следующий интервалы: толеранс к рассинхрону часов и задержке ввода).

**Replay.** Внутри окна один и тот же код валиден несколько раз; кит состояния
не хранит. Защита от повторного ввода — на стороне потребителя: запомнить
последний принятый код (или счётчик окна) на пользователя и отвергать повтор.

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
провайдер OTel — делегирующий: инструменты, созданные до `SetMeterProvider`,
переезжают на реальный провайдер при его регистрации, поэтому порядок
«конструктор → провайдер» не критичен. Полный справочник сигналов —
[observability.md](observability.md).

## fx и конфигурация

`Module` (`module.go`) провайдит `Hasher`, `*HS256JWT` и его интерфейсы
(`JWTSigner`, `JWTParser`) и `*Registry`. `Module` **не** провайдит сами
`Argon2idParams` и `JWTConfig` — их регистрирует приложение (типично через
App-структуру), однородно с `httpkit`/`pgkit`. Для приложений без App-pattern
есть envconfig-конструкторы `NewArgon2idParams` / `NewJWTConfig` / `NewTOTPConfig`
(читают ENV через `kelseyhightower/envconfig`). `TOTPGenerator` в граф fx не
входит — issuer доменно-специфичен, его создаёт потребитель напрямую.

Детали ENV — [configuration.md](configuration.md); рецепты подключения —
[usage.md](usage.md).
