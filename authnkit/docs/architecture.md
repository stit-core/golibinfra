# authnkit — устройство

`authnkit` — набор не связанных между собой примитивов аутентификации. Кит
сознательно не строит flow: нет сессий, refresh-ротации, account lockout,
enrollment'а MFA, хранилища секретов. Это «кирпичики», из которых потребитель
собирает свой auth-домен, и каждый из них криптографически корректен по
умолчанию.

```
authnkit/
├── hasher.go        Hasher: Argon2idHasher (хэш пароля)
├── jwt.go           JWTSigner/JWTParser: HS256JWT (подпись/разбор токена)
├── totp.go          TOTPGenerator (RFC 6238, обёртка pquerna/otp)
├── permissions.go   RBAC: Permission / RolePermissions / Has (in-process реестр)
├── metrics.go       OTel-инструменты (singleton через sync.Once)
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

WHY параметры зашиты в хэш: старые хэши проверяются корректно даже после того,
как `Argon2idParams` подняли (стойкость растёт со временем). Соль уникальна на
каждый `Hash` — два вызова с одним паролем дают разные строки. Невалидная
кодировка, чужой `v=`, битый base64 → `error` (и `false`), а не паника.

### JWT — HS256

`HS256JWT` реализует `JWTSigner` (`Sign`) и `JWTParser` (`Parse`) поверх
`github.com/golang-jwt/jwt/v5`. Алгоритм — симметричный HMAC-SHA256; секрет
общий для подписи и проверки (`JWTConfig.Secret`).

- `Sign(claims, ttl)` копирует переданные claims, проставляет `exp = now+ttl` и
  `iat = now`, подписывает `SigningMethodHS256`. TTL — **аргумент вызова**, не
  поле конфига: access и refresh токены выпускаются одним signer'ом с разным
  TTL.
- `Parse(token)` верифицирует подпись и срок. Парсер жёстко требует HMAC-метод
  (защита от alg-confusion: токен с `alg: none` или RS256 отвергается). Ошибки
  библиотеки нормализуются в стабильные sentinel'ы:
  `ErrTokenExpired` / `ErrTokenSignature` / `ErrTokenMalformed` — потребитель
  матчит их через `errors.Is`, не разбирая внутренние ошибки `jwt/v5`.

Claims на выходе — `map[string]any`. Числовые значения (`exp`, `iat`, любые
свои) приходят как `float64` — это поведение JSON-декодера `encoding/json`,
учитывай при чтении.

### TOTP — RFC 6238

`TOTPGenerator` — тонкая обёртка над `github.com/pquerna/otp/totp`. Хранит
только `Issuer` (имя сервиса для otpauth-URL).

- `Generate(accountName)` создаёт новый секрет и возвращает его base32-форму +
  `otpauth://`-URL (его можно отдать как QR в authenticator-приложение).
- `Verify(secret, code)` проверяет код. Параметры — дефолты библиотеки: период
  30 с, 6 цифр, SHA1, окно ±1 шаг (принимаются текущий, предыдущий и следующий
  интервалы — это толеранс к рассинхрону часов и задержке ввода).

### RBAC — in-process реестр прав

`permissions.go` — простейший статический RBAC: соответствие «роль → набор
прав» в одном process-global реестре под `sync.RWMutex`.

- `RegisterRolePermissions(role, perms...)` записывает набор прав роли (полностью
  перезаписывает предыдущий для этой роли).
- `Has(role, perm)` — потокобезопасная проверка под `RLock`. Неизвестная роль
  или право → `false` (fail-closed).

WHY in-process и статично: это базовый guard для ролей, известных на старте
приложения (типичный сценарий — `RegisterRolePermissions` в init/конструкторе).
Динамические права на пользователя, иерархии ролей, скоупы из БД — это уже
доменная логика потребителя, кит её не навязывает.

## Observability

Все четыре примитива читают глобальный OTel `MeterProvider` через один
singleton (`metrics.go`, `getInstruments` под `sync.Once`). WHY singleton:
повторные `NewHS256JWT` / `NewArgon2idHasher` не пытаются заново
зарегистрировать одни и те же имена инструментов и не ловят «instrument already
exists» в OTel SDK. Следствие-инвариант: `MeterProvider` должен быть установлен
**до** первого вызова функций кита (в fx-сборке это обеспечивает `telemetry.Module`).
Полный справочник сигналов — [observability.md](observability.md).

## fx и конфигурация

`Module` (`module.go`) провайдит `Hasher`, `*HS256JWT` и его интерфейсы
(`JWTSigner`, `JWTParser`). `Module` **не** провайдит сами `Argon2idParams` и
`JWTConfig` — их регистрирует приложение (типично через App-структуру),
однородно с `httpkit`/`pgkit`/`telemetry`. Для приложений без App-pattern есть
envconfig-конструкторы `NewArgon2idParams` / `NewJWTConfig` (читают ENV через
`kelseyhightower/envconfig`). `TOTPGenerator` и RBAC в граф fx не входят —
issuer и набор ролей доменно-специфичны, их создаёт потребитель напрямую.

Детали ENV — [configuration.md](configuration.md); рецепты подключения —
[usage.md](usage.md).
