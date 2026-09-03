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
func (h *Argon2idHasher) NeedsRehash(encoded string) bool

var ErrConfig, ErrInvalidHash error

// JWT
type JWTSigner interface {
	Sign(claims map[string]any, ttl time.Duration) (string, error)
}
type JWTParser interface {
	Parse(token string) (map[string]any, error)
}
func NewHS256JWT(cfg JWTConfig, opts ...Option) (*HS256JWT, error) // реализует и JWTSigner, и JWTParser
func (h *HS256JWT) SignAccess(claims map[string]any) (string, error)  // ttl = JWTConfig.AccessTTL
func (h *HS256JWT) SignRefresh(claims map[string]any) (string, error) // ttl = JWTConfig.RefreshTTL
func WithClock(c clockkit.Clock) Option

var ErrTokenExpired, ErrTokenMalformed, ErrTokenSignature error

// TOTP
func NewTOTP(cfg TOTPConfig, opts ...Option) *TOTPGenerator
func (g *TOTPGenerator) Generate(accountName string) (secret, otpauthURL string, err error)
func (g *TOTPGenerator) Verify(secret, code string) bool

// RBAC
type Permission string
func NewRegistry() *Registry
func (r *Registry) Register(role string, perms ...Permission)
func (r *Registry) Has(role string, p Permission) bool

// envconfig-конструкторы (для приложений без App-pattern)
func NewArgon2idParams() (Argon2idParams, error)
func NewJWTConfig() (JWTConfig, error)
func NewTOTPConfig() (TOTPConfig, error)
```

## Хэш пароля

```go
hasher := authnkit.NewArgon2idHasher(authnkit.Argon2idParams{
	Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
})

encoded, err := hasher.Hash(plainPassword) // храни encoded целиком, он самоописывающий
if err != nil {
	return err // ErrConfig — параметры хэшера вне окна
}

ok, err := hasher.Verify(submittedPassword, encoded)
// errors.Is(err, ErrInvalidHash) — encoded битый/чужого формата/вне окна (не «пароль неверен»)
// ok == false — пароль не совпал

if ok && hasher.NeedsRehash(encoded) {
	encoded, _ = hasher.Hash(submittedPassword) // параметры подняли — обновить запись
}
```

`encoded` хранится в БД как есть — соль и параметры внутри строки. Соль на
каждый `Hash` новая, поэтому одинаковые пароли дают разные строки.

## Sign / Verify JWT

```go
jwt, err := authnkit.NewHS256JWT(cfg) // cfg.Secret ≥ 32 байт, иначе ErrConfig
if err != nil {
	return err
}

access, _ := jwt.SignAccess(map[string]any{"sub": userID, "role": "admin"})
refresh, _ := jwt.SignRefresh(map[string]any{"sub": userID, "typ": "refresh"})
invite, _ := jwt.Sign(map[string]any{"sub": userID, "typ": "invite"}, 24*time.Hour) // нестандартный TTL

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
	// мусор на входе, не-HS256 alg, нет exp
}
```

`exp` и `iat` проставляются автоматически; переопределять их в `claims` не
нужно. Числа в разобранных claims приходят как `float64` (поведение
JSON-декодера) — приводи типы при чтении. Тип токена (`access` / `refresh`)
кит не различает — клади свой claim (например `typ`) и проверяй его в flow.

## TOTP (2FA)

```go
gen := authnkit.NewTOTP(authnkit.TOTPConfig{Issuer: "MyService", Skew: 1})

// enrollment: показать пользователю QR из otpauthURL, secret сохранить у себя
secret, otpauthURL, err := gen.Generate(user.Email)

// логин: проверить введённый код
if gen.Verify(secret, submittedCode) {
	// второй фактор пройден — запомни принятый код, чтобы отвергнуть его повтор
}
```

`Verify` принимает код для текущего и `±Skew` соседних 30-секундных окон
(толеранс к рассинхрону часов).

## RBAC

```go
// На старте приложения — наполнить реестр ролей (из fx: *authnkit.Registry).
roles := authnkit.NewRegistry()
roles.Register("admin", "users:read", "users:write")
roles.Register("viewer", "users:read")

// В обработчике — проверить право роли из токена.
if !roles.Has(role, authnkit.Permission("users:write")) {
	return http.StatusForbidden // fail-closed: неизвестная роль/право → false
}
```

Реестр потокобезопасен. Повторный `Register` для той же роли полностью
перезаписывает её набор прав.

## Подключение через fx

```go
import (
	"go.uber.org/fx"
	"git.stit.tech/stit-core/golibinfra/authnkit"
)

fx.New(
	authnkit.Module,  // provides: Hasher, *HS256JWT, JWTSigner, JWTParser, *Registry

	// Module НЕ provide'ит Argon2idParams и JWTConfig — регистрируем сами.
	fx.Provide(authnkit.NewArgon2idParams, authnkit.NewJWTConfig),

	fx.Invoke(func(h authnkit.Hasher, s authnkit.JWTSigner, p authnkit.JWTParser, r *authnkit.Registry) {
		r.Register("admin", "users:read", "users:write")
	}),
)
```

При App-pattern (как в `httpkit`/`pgkit`) `Argon2idParams` и
`JWTConfig` приходят из общей App-структуры — отдельный `fx.Provide` не нужен.
Короткий `JWT_SECRET` валит сборку графа (`ErrConfig`), а не первый `Sign`.

`TOTPGenerator` в граф fx не входит: issuer доменно-специфичен — создавай
`NewTOTP(...)` напрямую в коде сервиса (конфиг — `NewTOTPConfig()` из ENV).

## Инварианты и подводные камни

- **`Verify`-ошибка ≠ «пароль неверен».** `Hasher.Verify` возвращает `(false,
  ErrInvalidHash)` при битой/чужой кодировке и `(false, nil)` при несовпадении
  пароля. Логируй `err`, но пользователю всегда отдавай одинаковый «неверные
  учётные данные» (не выдавай, какой именно случай).
- **Окно параметров Argon2id.** PHC-строка с `t=0`, `p=0`, пустым хэшем или
  `m` вне `8 МиБ..1 ГиБ` → `ErrInvalidHash`; кит не даёт данным из хранилища
  управлять CPU/RAM процесса.
- **Стойкость Argon2id растёт со временем, безопасно для старых хэшей.** Параметры
  зашиты в PHC-строку, поэтому поднятие `Argon2idParams` не ломает проверку ранее
  выданных хэшей. Перевыпуск под новую стойкость — `NeedsRehash` + `Hash` на
  успешном логине, на стороне потребителя.
- **JWT: TTL из конфига или явно.** `SignAccess`/`SignRefresh` берут
  `JWTConfig.AccessTTL/RefreshTTL`; `Sign(claims, ttl)` — для нестандартных сроков.
- **JWT clock skew.** `Parse` принимает токен ещё `Leeway` (default 60s) после
  `exp` и допускает `nbf`/`iat` на `Leeway` в будущем. Часы между сервисами всё
  равно синхронизируй (NTP).
- **alg-confusion закрыт.** `Parse` принимает только `HS256`; токен с `alg: none`,
  асимметричным или другим HMAC-алгоритмом → `ErrTokenMalformed`. `exp`
  обязателен. Секрет HS256 общий для подписи и проверки — храни его как секрет,
  не короче 32 байт (иначе `ErrConfig`).
- **Числа в claims — `float64`.** Любые числовые значения разобранных claims
  декодируются как `float64`; приводи типы при чтении.
- **TOTP-окно ±Skew шагов.** С `Skew=1` код «живёт» ~90 с. От повторного
  использования одного кода (replay) кит не защищает: однократность кода на
  окно enforce'ит потребитель (хранить последний принятый код/окно на пользователя).
- **RBAC fail-closed и экземплярный.** Неизвестная роль/право → `false`. Реестр
  — экземпляр (`*Registry` из fx); process-global состояния нет.
