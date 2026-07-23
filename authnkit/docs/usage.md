# authnkit — рецепты

Точные сигнатуры публичного API и типовые сценарии.

## Сигнатуры

```go
// Hashing
type Hasher interface {
	Hash(plain string) (string, error)
	Verify(plain, encoded string) (bool, error)
}
func NewArgon2idHasher(p Argon2idParams) *Argon2idHasher

// JWT
type JWTSigner interface {
	Sign(claims map[string]any, ttl time.Duration) (string, error)
}
type JWTParser interface {
	Parse(token string) (map[string]any, error)
}
func NewHS256JWT(cfg JWTConfig) *HS256JWT // реализует и JWTSigner, и JWTParser

var ErrTokenExpired, ErrTokenMalformed, ErrTokenSignature error

// TOTP
func NewTOTP(issuer string) *TOTPGenerator
func (g *TOTPGenerator) Generate(accountName string) (secret, otpauthURL string, err error)
func (g *TOTPGenerator) Verify(secret, code string) bool

// RBAC
type Permission string
func RegisterRolePermissions(role string, perms ...Permission)
func Has(role string, p Permission) bool

// envconfig-конструкторы (для приложений без App-pattern)
func NewArgon2idParams() (Argon2idParams, error)
func NewJWTConfig() (JWTConfig, error)
```

## Хэш пароля

```go
hasher := authnkit.NewArgon2idHasher(authnkit.Argon2idParams{
	Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
})

encoded, err := hasher.Hash(plainPassword) // храни encoded целиком, он самоописывающий
if err != nil {
	return err
}

ok, err := hasher.Verify(submittedPassword, encoded)
// err != nil  — encoded битый/чужого формата (не «пароль неверен»)
// ok == false — пароль не совпал
```

`encoded` хранится в БД как есть — соль и параметры внутри строки. Соль на
каждый `Hash` новая, поэтому одинаковые пароли дают разные строки.

## Sign / Verify JWT

```go
jwt := authnkit.NewHS256JWT(cfg) // cfg.Secret обязателен

access, _ := jwt.Sign(map[string]any{"sub": userID, "role": "admin"}, cfg.AccessTTL)
refresh, _ := jwt.Sign(map[string]any{"sub": userID, "typ": "refresh"}, cfg.RefreshTTL)

claims, err := jwt.Parse(access)
switch {
case err == nil:
	sub, _ := claims["sub"].(string)
	_ = sub
case errors.Is(err, authnkit.ErrTokenExpired):
	// отправить на refresh
case errors.Is(err, authnkit.ErrTokenSignature):
	// чужой/подделанный токен
case errors.Is(err, authnkit.ErrTokenMalformed):
	// мусор на входе
}
```

`exp` и `iat` проставляются автоматически в `Sign`; переопределять их в `claims`
не нужно. Числа в разобранных claims приходят как `float64` (поведение
JSON-декодера) — приводи типы при чтении.

## TOTP (2FA)

```go
gen := authnkit.NewTOTP("MyService") // issuer — имя в authenticator-приложении

// enrollment: показать пользователю QR из otpauthURL, secret сохранить у себя
secret, otpauthURL, err := gen.Generate(user.Email)

// логин: проверить введённый код
if gen.Verify(secret, submittedCode) {
	// второй фактор пройден
}
```

`Verify` принимает код для текущего и соседних 30-секундных окон (толеранс к
рассинхрону часов).

## RBAC

```go
// На старте приложения — наполнить реестр ролей.
authnkit.RegisterRolePermissions("admin", "users:read", "users:write")
authnkit.RegisterRolePermissions("viewer", "users:read")

// В обработчике — проверить право роли из токена.
if !authnkit.Has(role, authnkit.Permission("users:write")) {
	return http.StatusForbidden // fail-closed: неизвестная роль/право → false
}
```

Реестр process-global и потокобезопасен. Повторный `RegisterRolePermissions` для
той же роли полностью перезаписывает её набор прав.

## Подключение через fx

```go
import (
	"go.uber.org/fx"
	"git.stit.tech/stit-core/golibinfra/authnkit"
)

fx.New(
	authnkit.Module,  // provides: Hasher, *HS256JWT, JWTSigner, JWTParser

	// Module НЕ provide'ит Argon2idParams и JWTConfig — регистрируем сами.
	fx.Provide(authnkit.NewArgon2idParams, authnkit.NewJWTConfig),

	fx.Invoke(func(h authnkit.Hasher, s authnkit.JWTSigner, p authnkit.JWTParser) {
		// h, s, p готовы к использованию
	}),
)
```

При App-pattern (как в `httpkit`/`pgkit`) `Argon2idParams` и
`JWTConfig` приходят из общей App-структуры — отдельный `fx.Provide` не нужен.

`TOTPGenerator` и RBAC в граф fx не входят: issuer и набор ролей
доменно-специфичны — создавай `NewTOTP(...)` и зови `RegisterRolePermissions`
напрямую в коде сервиса.

## Инварианты и подводные камни

- **MeterProvider до первого вызова.** Инструменты привязываются к активному
  глобальному `MeterProvider` на первом обращении к киту (`sync.Once`). Если
  потребитель регистрирует свой `MeterProvider`, сделай это раньше первого
  хэша/подписи/верификации — иначе метрики уйдут в no-op.
- **`Verify`-ошибка ≠ «пароль неверен».** `Hasher.Verify` возвращает `(false,
  err)` при битой/чужой кодировке и `(false, nil)` при несовпадении пароля.
  Логируй `err`, но пользователю всегда отдавай одинаковый «неверные учётные
  данные» (не выдавай, какой именно случай).
- **Стойкость Argon2id растёт со временем, безопасно для старых хэшей.** Параметры
  зашиты в PHC-строку, поэтому поднятие `Argon2idParams` не ломает проверку ранее
  выданных хэшей. Перевыпуск под новую стойкость (re-hash на успешном логине) —
  на стороне потребителя, кит его не делает.
- **JWT: TTL — аргумент, не конфиг.** `exp` берётся из `ttl` в `Sign`, а не из
  `JWTConfig.AccessTTL/RefreshTTL` (это именованные значения для приложения).
  Передавай нужный TTL явно.
- **JWT clock skew.** `Parse` отвергает токен сразу по `exp` (`ErrTokenExpired`)
  без grace-периода. Часы между сервисами синхронизируй (NTP); толеранс к
  рассинхрону — забота инфраструктуры, не кита.
- **alg-confusion закрыт.** `Parse` принимает только HMAC; токен с `alg: none`
  или асимметричным алгоритмом → `ErrTokenMalformed`. Секрет HS256 общий для
  подписи и проверки — храни его как секрет, не короче 32 байт.
- **Числа в claims — `float64`.** Любые числовые значения разобранных claims
  декодируются как `float64`; приводи типы при чтении.
- **TOTP-окно ±1 шаг.** Принимаются текущий, предыдущий и следующий 30-секундные
  интервалы — код «живёт» ~90 с. От повторного использования одного кода
  (replay) кит не защищает: однократность кода/секрета enforce'ит потребитель.
- **RBAC fail-closed и process-global.** Неизвестная роль/право → `false`.
  Реестр один на процесс; в параллельных тестах сохраняй/восстанавливай его
  состояние.
