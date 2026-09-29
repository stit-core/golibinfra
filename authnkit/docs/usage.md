# authnkit — рецепты

Точные сигнатуры публичного API и типовые сценарии.

## Сигнатуры

```go
// Hashing
type Hasher interface {
	Hash(ctx context.Context, plain string) (string, error)
	Verify(ctx context.Context, plain, encoded string) (bool, error)
	NeedsRehash(encoded string) bool
	VerifyDummy(ctx context.Context)
}
func NewArgon2idHasher(p Argon2idParams) (*Argon2idHasher, error)
func (p Argon2idParams) Validate() error

var ErrConfig, ErrInvalidHash, ErrHashParams error // ErrHashParams заворачивает ErrInvalidHash

// JWT
type JWTSigner interface {
	Sign(ctx context.Context, claims map[string]any, ttl time.Duration) (string, error)
	SignAccess(ctx context.Context, claims map[string]any) (string, error)  // ttl = JWTConfig.AccessTTL
	SignRefresh(ctx context.Context, claims map[string]any) (string, error) // ttl = JWTConfig.RefreshTTL
}
type JWTParser interface {
	Parse(ctx context.Context, token string) (map[string]any, error)        // тип токена не проверяется
	ParseAccess(ctx context.Context, token string) (map[string]any, error)  // typ == "access"
	ParseRefresh(ctx context.Context, token string) (map[string]any, error) // typ == "refresh"
}
func NewHS256JWT(cfg JWTConfig, opts ...JWTOption) (*HS256JWT, error) // реализует и JWTSigner, и JWTParser
func (c JWTConfig) Validate() error
func WithClock(c clockkit.Clock) ClockOption // JWTOption + TOTPOption + UsedCodeStoreOption
func WithIssuer(issuer string) JWTOption     // только NewHS256JWT
func WithAudience(aud ...string) JWTOption   // только NewHS256JWT

const ClaimTokenType = "typ" // значения TokenTypeAccess / TokenTypeRefresh
var ErrTokenExpired, ErrTokenMalformed, ErrTokenSignature, ErrWrongTokenType, ErrTokenAudience error

// TOTP
func NewTOTP(cfg TOTPConfig, opts ...TOTPOption) (*TOTPGenerator, error)
func (c TOTPConfig) Validate() error
func (g *TOTPGenerator) Generate(accountName string) (secret, otpauthURL string, err error)
func (g *TOTPGenerator) Verify(ctx context.Context, secret, code string) (bool, error)
func (g *TOTPGenerator) VerifyOnce(ctx context.Context, secret, code string, store UsedCodeStore) (bool, error)

type UsedCodeStore interface {
	MarkUsed(ctx context.Context, key string, ttl time.Duration) (bool, error)
}
func NewMemoryUsedCodeStore(opts ...UsedCodeStoreOption) *MemoryUsedCodeStore

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

`ctx` принимают методы, которые эмитят метрики: он несёт baggage и exemplar'ы
в записанные измерения. У `Hash`, `Verify` и `VerifyDummy` он ещё и прерывает
ожидание слота вычислений (не больше `max(1, GOMAXPROCS/Threads)` Argon2id
одновременно): `Hash`/`Verify` возвращают ошибку с `ctx.Err()`, `VerifyDummy`
пропускает вычисление. `NeedsRehash` и `Generate` сигналов не пишут и ctx не
берут.

## Хэш пароля

```go
hasher, err := authnkit.NewArgon2idHasher(authnkit.Argon2idParams{
	Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
})
if err != nil {
	return err // ErrConfig — параметры вне окна, падаем на старте
}

encoded, err := hasher.Hash(ctx, plainPassword) // храни encoded целиком, он самоописывающий
if err != nil {
	return err
}

ok, err := hasher.Verify(ctx, submittedPassword, encoded)
// errors.Is(err, ErrInvalidHash) — encoded битый/чужого формата/вне окна (не «пароль неверен»)
// ok == false — пароль не совпал

if ok && hasher.NeedsRehash(encoded) {
	encoded, _ = hasher.Hash(ctx, submittedPassword) // параметры подняли — обновить запись
}
```

`encoded` хранится в БД как есть — соль и параметры внутри строки. Соль на
каждый `Hash` новая, поэтому одинаковые пароли дают разные строки.

## Логин без timing-оракула

Путь «пользователя нет» отвечает за микросекунды, путь «пароль неверен» — за
десятки-сотни миллисекунд: разница видна снаружи и перечисляет учётные записи.
`VerifyDummy` выравнивает время.

```go
user, err := repo.ByEmail(ctx, email)
if errors.Is(err, ErrNotFound) {
	hasher.VerifyDummy(ctx) // столько же CPU, сколько Verify
	return ErrInvalidCredentials
}
ok, err := hasher.Verify(ctx, submittedPassword, user.PasswordHash)
if err != nil || !ok {
	return ErrInvalidCredentials // одинаковый ответ на оба случая
}
```

## Sign / Verify JWT

```go
jwt, err := authnkit.NewHS256JWT(cfg) // cfg.Secret ≥ 32 байт, иначе ErrConfig
if err != nil {
	return err
}

access, _ := jwt.SignAccess(ctx, map[string]any{"sub": userID, "role": "admin"})
refresh, _ := jwt.SignRefresh(ctx, map[string]any{"sub": userID})
invite, _ := jwt.Sign(ctx, map[string]any{"sub": userID, "typ": "invite"}, 24*time.Hour) // нестандартный TTL

claims, err := jwt.ParseAccess(ctx, access)
switch {
case err == nil:
	sub, _ := claims["sub"].(string)
	_ = sub
case errors.Is(err, authnkit.ErrWrongTokenType):
	// предъявили refresh (или чужой) токен там, где нужен access
case errors.Is(err, authnkit.ErrTokenExpired):
	// отправить на refresh
case errors.Is(err, authnkit.ErrTokenSignature):
	// чужой/подделанный токен
case errors.Is(err, authnkit.ErrTokenAudience):
	// токен другого сервиса на общем секрете
case errors.Is(err, authnkit.ErrTokenMalformed):
	// мусор на входе, не-HS256 alg, нет exp
}
```

`exp`, `iat` и (для `SignAccess`/`SignRefresh`) `typ` **перезаписываются**
китом — значения из `claims` для этих ключей не сохраняются. Числа в
разобранных claims приходят как `float64` (поведение JSON-декодера) — приводи
типы при чтении.

Токены со своей схемой (приглашения, одноразовые ссылки) выпускай через
`Sign` со своим `typ` и разбирай через `Parse`, проверяя `typ` в коде flow'а:
`ParseAccess`/`ParseRefresh` пропускают только собственные типы кита.

### Изоляция по iss/aud

Общий `JWT_SECRET` на несколько сервисов означает, что подпись сходится
везде. `WithIssuer`/`WithAudience` привязывают токен к выпускающему и
принимающему:

```go
issuer, _ := authnkit.NewHS256JWT(cfg, authnkit.WithIssuer("auth"), authnkit.WithAudience("billing"))
// billing принимает только токены с aud=billing
billing, _ := authnkit.NewHS256JWT(cfg, authnkit.WithAudience("billing"))
```

Опции симметричны: заданный `iss`/`aud` и проставляется при `Sign*`, и
требуется при `Parse*` (иначе `ErrTokenAudience`). Не заданы — не проверяются.

## TOTP (2FA)

```go
gen, err := authnkit.NewTOTP(authnkit.TOTPConfig{Issuer: "MyService", Skew: 1})
if err != nil {
	return err // ErrConfig: пустой issuer, ':'/'/' в нём или Skew > 2
}

// enrollment: показать пользователю QR из otpauthURL, secret сохранить у себя
secret, otpauthURL, err := gen.Generate(user.Email)

// логин: проверить введённый код
ok, err := gen.Verify(ctx, secret, submittedCode)
switch {
case err != nil:
	// секрет в хранилище нечитаем — это не «неверный код», а поломка данных
case ok:
	// второй фактор пройден
}
```

`Verify` принимает код для текущего и `±Skew` соседних 30-секундных окон
(толеранс к рассинхрону часов).

### Однократность кода (replay)

`VerifyOnce` отвергает код, уже принятый в пределах его окна:

```go
store := authnkit.NewMemoryUsedCodeStore() // одна реплика
ok, err := gen.VerifyOnce(ctx, secret, submittedCode, store)
```

Нескольким репликам нужен общий store — например поверх `cachekit.Cache`:

```go
type redisUsedCodes struct{ c cachekit.Cache }

func (r redisUsedCodes) MarkUsed(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return r.c.SetNX(ctx, key, []byte("1"), ttl)
}
```

Ключ кит считает сам (SHA-256 от секрета и кода), TTL — длина окна с учётом
`Skew`. Ошибка store → `(false, error)`: пускать ли пользователя при
недоступном хранилище, решает потребитель.

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
	// provides: Hasher, *Argon2idHasher, *HS256JWT, JWTSigner, JWTParser,
	//           *TOTPGenerator, *Registry
	authnkit.Module,

	// Module НЕ provide'ит Argon2idParams, JWTConfig и TOTPConfig — регистрируем сами.
	fx.Provide(authnkit.NewArgon2idParams, authnkit.NewJWTConfig, authnkit.NewTOTPConfig),

	fx.Invoke(func(h authnkit.Hasher, s authnkit.JWTSigner, p authnkit.JWTParser, r *authnkit.Registry) {
		r.Register("admin", "users:read", "users:write")
	}),
)
```

При App-pattern (как в `httpkit`/`pgkit`) конфиги приходят из общей
App-структуры — отдельный `fx.Provide` не нужен. Невалидный конфиг валит
сборку графа (`ErrConfig`), а не первый `Hash`/`Sign`. `*TOTPGenerator`
строится лениво: `TOTPConfig` нужен только приложению, которое его запрашивает.

## Инварианты и подводные камни

- **`Verify`-ошибка ≠ «пароль неверен».** `Hasher.Verify` возвращает `(false,
  ErrInvalidHash)` при битой/чужой кодировке и `(false, nil)` при несовпадении
  пароля. Логируй `err`, но пользователю всегда отдавай одинаковый «неверные
  учётные данные» (не выдавай, какой именно случай).
- **Окно параметров Argon2id.** PHC-строка вне окна (`t` вне `1..64`, `p` вне
  `1..16`, `m` вне `8 МиБ..1 ГиБ`, произведение `t*m` больше
  `MaxCostKiBPasses` = 786432 КиБ·прохода, соль/хэш вне `8..64`/`16..128`
  байт) → `ErrHashParams` (он заворачивает `ErrInvalidHash`) **до** вычисления.
  Потолок стоимости ограничивает и CPU, и RAM одной проверки: строка из
  хранилища стоит не больше четырёх дефолтных хэшей (≈0.2 с и ≈768 МиБ на
  пике), а не десять секунд и гигабайт, как позволяли бы `t` и `m` по
  отдельности. Строка должна быть канонической — пробелы, переводы строк,
  ведущие нули в числах и неканоничный хвост base64 отвергаются.
- **Неизвестный пользователь — `VerifyDummy`.** Иначе время ответа перечисляет
  учётные записи.
- **Argon2id считается в слотах.** Одновременно — не больше
  `max(1, GOMAXPROCS/Threads)` вычислений на хэшер: наплыв анонимных логинов
  ждёт в очереди, а не множит по `ARGON2_MEMORY_KIB` на запрос. Ожидание
  ограничено `ctx` запроса — передавай его, а не `context.Background()`.
- **Стойкость Argon2id растёт со временем, безопасно для старых хэшей.** Параметры
  зашиты в PHC-строку, поэтому поднятие `Argon2idParams` не ломает проверку ранее
  выданных хэшей. Перевыпуск под новую стойкость — `NeedsRehash` + `Hash` на
  успешном логине, на стороне потребителя.
- **Тип токена — часть контракта.** `SignAccess`/`SignRefresh` пишут
  `typ=access|refresh`, `ParseAccess`/`ParseRefresh` требуют совпадения:
  refresh-токен в access-контуре → `ErrWrongTokenType`. `Parse` тип не
  проверяет — используй его только для собственных схем токенов.
- **JWT: TTL из конфига или явно.** `Sign(ctx, claims, ttl)` с `ttl <= 0` →
  `ErrConfig` (а не токен, живущий один leeway).
- **JWT clock skew.** `Parse*` принимает токен ещё `Leeway` (default 60s) после
  `exp` и допускает `nbf`/`iat` на `Leeway` в будущем. Часы между сервисами всё
  равно синхронизируй (NTP).
- **alg-confusion закрыт.** `Parse*` принимает только `HS256`; токен с `alg: none`,
  асимметричным или другим HMAC-алгоритмом → `ErrTokenMalformed`. `exp`
  обязателен. Секрет HS256 общий для подписи и проверки — храни его как секрет,
  не короче 32 байт (иначе `ErrConfig`).
- **Числа в claims — `float64`.** Любые числовые значения разобранных claims
  декодируются как `float64`; приводи типы при чтении.
- **TOTP-окно ±Skew шагов.** С `Skew=1` код «живёт» ~90 с; потолок `Skew` — 2
  (`MaxTOTPSkew`), выше окно brute-force растёт линейно. Однократность кода
  даёт `VerifyOnce` с `UsedCodeStore`; голый `Verify` от replay не защищает.
- **RBAC fail-closed и экземплярный.** Неизвестная роль/право → `false`. Реестр
  — экземпляр (`*Registry` из fx); process-global состояния нет.
