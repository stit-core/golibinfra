# authnkit — рецепты

Точные сигнатуры публичного API и типовые сценарии.

## Сигнатуры

```go
// Hashing
type Hasher interface {
	Hash(ctx context.Context, plain string) (string, error)
	Verify(ctx context.Context, plain, encoded string) (bool, error)
	NeedsRehash(encoded string) bool
	VerifyDummy(ctx context.Context) error // ошибка — как у Verify: ErrBusy, отмена ctx
}
func NewArgon2idHasher(p Argon2idConfig) (*Argon2idHasher, error)
func (p Argon2idConfig) Validate() error

var ErrConfig, ErrInvalidHash, ErrHashParams, ErrPasswordTooLong error // ErrHashParams заворачивает ErrInvalidHash
var ErrBusy error // очередь Argon2id полна; RetryAfter() time.Duration — секунда
const MaxPasswordLen = 1024 // байт; длиннее — ErrPasswordTooLong у Hash и Verify

// JWT
type JWTSigner interface {
	Sign(ctx context.Context, claims map[string]any, ttl time.Duration) (string, error)
	SignAccess(ctx context.Context, claims map[string]any) (string, error) // ttl = JWTConfig.AccessTTL
}
type JWTParser interface {
	Parse(ctx context.Context, token string) (map[string]any, error)       // тип токена не проверяется
	ParseAccess(ctx context.Context, token string) (map[string]any, error) // typ == "access"
}
func NewHS256JWT(cfg JWTConfig, opts ...JWTOption) (*HS256JWT, error) // реализует и JWTSigner, и JWTParser
func (c JWTConfig) Validate() error
func WithClock(c clockkit.Clock) ClockOption // JWTOption + TOTPOption + UsedCodeStoreOption

const ClaimTokenType = "typ" // значение TokenTypeAccess
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

// Одноразовые токены
func NewToken() string               // 256 бит из crypto/rand, hex (64 символа)
func HashToken(token string) string  // SHA-256 в hex — для хранения и поиска

// Сессии refresh — authnkit/session и authnkit/session/pgstore
type Store interface { // session.Store
	Issue(ctx context.Context, subject string) (Token, error)
	Rotate(ctx context.Context, refresh string) (Token, error)
	Revoke(ctx context.Context, refresh string) (subject string, err error) // владелец закрытой этим вызовом сессии
	RevokeAll(ctx context.Context, subject string) error
}
type Token struct{ Value, SessionID, Subject string; ExpiresAt, SessionExpiresAt time.Time }
type ReusedError struct{ SessionID, Subject string } // errors.Is(err, ErrReused)
var ErrConfig, ErrInvalidSubject, ErrInvalid, ErrExpired, ErrReused, ErrInTx error
func NewConfig() (Config, error) // SESSION_*
func CheckSubject(subject string) error
func pgstore.NewStore(cfg session.Config, db *pgkit.DB, opts ...pgstore.Option) (*pgstore.Store, error)
func pgstore.WithClock(c clockkit.Clock) pgstore.Option
func (s *pgstore.Store) Cleanup(ctx context.Context) error
var pgstore.Module, pgstore.Migrations

// RBAC
type Permission string
func NewRegistry() *Registry
func (r *Registry) Register(role string, perms ...Permission)
func (r *Registry) Has(role string, p Permission) bool

// конструкторы конфигов из ENV (для приложений без App-pattern)
func NewArgon2idConfig() (Argon2idConfig, error)
func NewJWTConfig() (JWTConfig, error)
func NewTOTPConfig() (TOTPConfig, error)
```

`ctx` принимают методы, которые эмитят метрики: он несёт baggage и exemplar'ы
в записанные измерения. У `Hash`, `Verify` и `VerifyDummy` он ещё и прерывает
ожидание слота вычислений (не больше `max(1, GOMAXPROCS/Threads)` Argon2id
одновременно): все три возвращают ошибку с `ctx.Err()`. Слот ждут не больше
восьми вызовов на слот — при полной очереди все три сразу отдают `ErrBusy`.
`NeedsRehash` и `Generate` сигналов не пишут и ctx не берут.

## Хэш пароля

```go
hasher, err := authnkit.NewArgon2idHasher(authnkit.Argon2idConfig{
	Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
})
if err != nil {
	return err // ErrConfig — параметры вне окна, падаем на старте
}

encoded, err := hasher.Hash(ctx, plainPassword) // храни encoded целиком, он самоописывающий
if err != nil {
	return err // errors.Is(err, ErrPasswordTooLong) — ошибка ввода: ответ 422, а не 500
	//            errors.Is(err, ErrBusy) — очередь хэшера полна: ответ 503 с Retry-After
}

ok, err := hasher.Verify(ctx, submittedPassword, encoded)
// errors.Is(err, ErrInvalidHash) — encoded битый/чужого формата/вне окна (не «пароль неверен»)
// errors.Is(err, ErrPasswordTooLong) — пароль длиннее MaxPasswordLen (в байтах)
// errors.Is(err, ErrBusy) — очередь хэшера полна: перегрузка, ответ 503 с Retry-After
// ok == false — пароль не совпал

if ok && hasher.NeedsRehash(encoded) {
	encoded, _ = hasher.Hash(ctx, submittedPassword) // параметры подняли — обновить запись
}
```

`encoded` хранится в БД как есть — соль и параметры внутри строки. Соль на
каждый `Hash` новая, поэтому одинаковые пароли дают разные строки.

Пароль длиннее `MaxPasswordLen` (1024 байта) в Argon2id не попадает: `Hash` отказывает
сразу, `Verify` — после холостого вычисления (`VerifyDummy`), поэтому длина не выдаёт
учётную запись таймингом. `ErrPasswordTooLong` — ошибка ввода, а не сбой сервиса: на
регистрации, смене и сбросе пароля потребитель отвечает на неё 422, на входе — тем же, что
на неверный пароль.

`maxLength:"1024"` у поля DTO — ранний отказ, а не замена этой обработки: huma считает
`maxLength` в символах, предел кита — в байтах. Пароль не из ASCII короче 1024 символов
бывает длиннее 1024 байт (кириллица — 2 байта на символ): DTO его пропускает, `Hash`
отвергает.

## Логин без timing-оракула

Путь «пользователя нет» отвечает за микросекунды, путь «пароль неверен» — за
десятки-сотни миллисекунд: разница видна снаружи и перечисляет учётные записи.
`VerifyDummy` выравнивает время.

```go
user, err := repo.ByEmail(ctx, email)
if errors.Is(err, ErrNotFound) {
	if err := hasher.VerifyDummy(ctx); err != nil { // столько же CPU, сколько Verify, в той же очереди
		return err // ErrBusy, отмена ctx — тот же ответ, что на ошибку Verify ниже
	}
	return ErrInvalidCredentials
}
ok, err := hasher.Verify(ctx, submittedPassword, user.PasswordHash)
switch {
case err != nil && !errors.Is(err, authnkit.ErrPasswordTooLong) && !errors.Is(err, authnkit.ErrInvalidHash):
	return err // ErrBusy, отмена ctx — как у VerifyDummy выше; ErrBusy — 503 с Retry-After
case err != nil || !ok:
	return ErrInvalidCredentials // одинаковый ответ на оба случая
}
```

Отказ `ErrBusy` и отмена `ctx` в очереди — не неверный пароль: проверки не было, и ответ на
них один на обоих путях (на `ErrBusy` — 503 с `Retry-After`), а не «неверные учётные данные».
Иначе под нагрузкой путь без учётной записи и путь с ней отвечали бы по-разному, а
пользователь с верным паролем получал бы отказ входа.

Попытку, которую guard входа (`bruteforcekit.Guard.Attempt`) учёл до проверки, на `ErrBusy`
возвращают — `Guard.Refund(ctx, subject, ip)`, затем 503: проверки секрета не было, и без
возврата клиент, повторяющий отказ, за `BRUTEFORCE_MAX_FAILURES` отказов блокирует свою пару,
не проверив пароль ни разу. Перебор от возврата лишних попыток не получает: к проверке доходит
не больше `BRUTEFORCE_MAX_FAILURES` попыток окна.

## Sign / Verify JWT

```go
jwt, err := authnkit.NewHS256JWT(cfg) // cfg.Secret ≥ 32 байт, иначе ErrConfig
if err != nil {
	return err
}

access, _ := jwt.SignAccess(ctx, map[string]any{"sub": userID, "role": "admin"})
invite, _ := jwt.Sign(ctx, map[string]any{"sub": userID, "typ": "invite"}, 24*time.Hour) // нестандартный TTL

claims, err := jwt.ParseAccess(ctx, access)
switch {
case err == nil:
	sub, _ := claims["sub"].(string)
	_ = sub
case errors.Is(err, authnkit.ErrWrongTokenType):
	// токен другой схемы (typ не access) там, где нужен access
case errors.Is(err, authnkit.ErrTokenExpired):
	// отправить на refresh (session.Store.Rotate)
case errors.Is(err, authnkit.ErrTokenSignature):
	// чужой/подделанный токен
case errors.Is(err, authnkit.ErrTokenAudience):
	// токен другого сервиса на общем секрете
case errors.Is(err, authnkit.ErrTokenMalformed):
	// мусор на входе, не-HS256 alg, нет exp
}
```

`exp`, `iat` и (для `SignAccess`) `typ` **перезаписываются**
китом — значения из `claims` для этих ключей не сохраняются. Числа в
разобранных claims приходят как `float64` (поведение JSON-декодера) — приводи
типы при чтении.

Токены со своей схемой (приглашения, одноразовые ссылки) выпускай через
`Sign` со своим `typ` и разбирай через `Parse`, проверяя `typ` в коде flow'а:
`ParseAccess` пропускает только `typ=access`.

### Изоляция по iss/aud

Общий `JWT_SECRET` на несколько сервисов означает, что подпись сходится
везде. `JWT_ISSUER`/`JWT_AUDIENCE` (`JWTConfig.Issuer`/`Audience`) привязывают
токен к выпускающему и получателям — у `Module` так же, как у `NewHS256JWT`:

```bash
# auth выпускает токены для billing и users
JWT_ISSUER=auth JWT_AUDIENCE=billing,users
# billing принимает только токены с aud=billing (и iss=auth)
JWT_ISSUER=auth JWT_AUDIENCE=billing
```

`iss`/`aud` симметричны: заданные и проставляются при `Sign*`, и требуются при
`Parse*` (иначе `ErrTokenAudience`): `iss` — совпадение, `aud` — пересечение. Не
заданы — не проверяются; без `JWT_AUDIENCE` сервисы на общем секрете принимают
токены друг друга. Смена секрета без разлогина — `JWT_PREVIOUS_SECRET`
([configuration.md § Ротация секрета](configuration.md#ротация-секрета)).

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

## Одноразовые токены

Ссылка сброса пароля, подтверждение email, refresh-сессия — токен из
`NewToken`: 256 бит из `crypto/rand` в hex. Пользователь получает токен, база
хранит `HashToken(token)` (SHA-256): утечка таблицы не даёт действующих токенов.
Медленный хеш не нужен — у токена полная энтропия.

```go
tok := authnkit.NewToken()
_, err := db.Exec(ctx, `INSERT INTO password_resets (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
	userID, authnkit.HashToken(tok), expiresAt)
// письмо с tok; при погашении — поиск по хешу и условный UPDATE в одной транзакции:
// UPDATE password_resets SET used_at = NOW() WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW()
```

Поиск — по хешу в SQL владельца таблицы (`WHERE token_hash = $1`); если хеш
сравнивается в памяти — `subtle.ConstantTimeCompare`. Погашение — условным
`UPDATE … WHERE used_at IS NULL`: из параллельных запросов побеждает один. Hex
длиной 64 logkit маскирует по умолчанию, base64url — нет: свой формат токена
не заводите.

## Сессии refresh

Refresh — не JWT, а непрозрачный токен в сессии `authnkit/session`: вход
открывает сессию с абсолютным сроком (`SESSION_ABSOLUTE_TTL`), каждое обновление
гасит токен и выдаёт следующий (`SESSION_IDLE_TTL`, не дальше срока сессии).
Хранилище — `session/pgstore` (Postgres, миграции кита), фейк для юнит-тестов —
`testkit/sessiontest`.

```go
// Вход — одной транзакцией под блокировкой строки пользователя: проверка пароля и статуса и
// Issue видят одно состояние. Без блокировки вход, прочитавший пользователя до COMMIT смены
// пароля, выдал бы сессию уже после RevokeAll — и она пережила бы «выйти везде».
var (
	refresh session.Token
	u       User
)
err := tx.WithTx(ctx, func(ctx context.Context) (err error) {
	if u, err = users.LockByLogin(ctx, login); err != nil { // SELECT … FOR SHARE: ждёт COMMIT смены пароля и статуса
		return err
	}
	if ok, err := hasher.Verify(ctx, password, u.PasswordHash); err != nil || !ok || u.Blocked {
		return errInvalidCredentials
	}
	refresh, err = sessions.Issue(ctx, u.ID) // в транзакции из ctx; refresh.Value — клиенту (cookie)
	return err
})
if err != nil {
	return err
}
access, err := jwt.SignAccess(ctx, map[string]any{"sub": u.ID, "role": u.Role})
```

```go
// Обновление: ротация вне транзакции, затем проверка, что пользователь ещё может входить. Сбой после Rotate
// (users.Get, выпуск access-токена) или потерянный ответ клиент повторяет тем же cookie: в окне
// SESSION_RETRY_WINDOW Rotate выдаёт нового преемника, а не отзывает сессию.
next, err := sessions.Rotate(ctx, refreshCookie)
var reused *session.ReusedError
switch {
case errors.As(err, &reused):
	// повтор использованного токена вне окна: сессия уже отозвана — аудит reused.Subject
	return errLoginAgain
case errors.Is(err, session.ErrInvalid), errors.Is(err, session.ErrExpired):
	return errLoginAgain
case err != nil:
	return err
}
u, err := users.Get(ctx, next.Subject) // заблокирован → отказ, next не выдавать

// Выход: владелец сессии — автор записи аудита; "" — токен неизвестен или сессия уже закрыта.
owner, err := sessions.Revoke(ctx, refreshCookie)
if err == nil && owner != "" {
	err = audit.Log(ctx, auditkit.Entry{ActorID: owner, Action: "logout", Resource: "session"})
}

// Отзыв всех сессий — в транзакции смены пароля или статуса: UPDATE строки пользователя
// конфликтует с FOR SHARE входа, и параллельный вход ждёт её COMMIT.
err = tx.WithTx(ctx, func(ctx context.Context) error {
	if err := users.Block(ctx, userID); err != nil {
		return err
	}
	return sessions.RevokeAll(ctx, userID) // откат блокировки откатит и отзыв
})
```

Вход держит соединение пула на время Argon2id (≈100 мс) — это цена порядка: проверка пароля
под блокировкой строки и `Issue` в той же транзакции. Вход без неё оставляет окно длиной в
проверку пароля, в котором `RevokeAll` смены пароля уже прошёл, а сессия по старому паролю ещё
выдаётся и живёт до `SESSION_ABSOLUTE_TTL`.

В fx — `pgstore.Module` (нужны `pgkit.Module`, `timerkit.Module` и
`fx.Provide(session.NewConfig)`); его миграции — источник `pgstore.Migrations`
для `pgkit.MigrateModule` или отдельной команды миграций: без них старт падает
(`pgkit.ErrSchema`, проверка `pgkit.CheckSchemaOnStart`). CLI без fx — `pgstore.NewStore(cfg, db)`,
схему сверяет `pgkit.CheckSchema(ctx, db, pgstore.Migrations)`.

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

	// Module НЕ provide'ит Argon2idConfig, JWTConfig и TOTPConfig — регистрируем сами.
	fx.Provide(authnkit.NewArgon2idConfig, authnkit.NewJWTConfig, authnkit.NewTOTPConfig),

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
  ErrInvalidHash)` при битой/чужой кодировке, `(false, ErrPasswordTooLong)` при пароле
  длиннее `MaxPasswordLen` и `(false, nil)` при несовпадении пароля. Логируй `err`, но
  пользователю всегда отдавай одинаковый «неверные учётные данные» (не выдавай, какой
  именно случай). Исключение — `ErrBusy`: перегрузка, ответ 503 на всех путях пароля.
- **`maxLength` DTO не заменяет обработку `ErrPasswordTooLong`.** Предел
  `MaxPasswordLen` — в байтах, `maxLength` huma — в символах: пароль не из ASCII проходит
  DTO и получает `ErrPasswordTooLong` из `Hash`. Без правила ошибки на него регистрация,
  смена и сброс пароля отвечают 500 на ввод пользователя — нужен ответ 422.
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
  учётные записи. Его ошибка (`ErrBusy`, отмена `ctx`) — тот же ответ, что на ошибку
  `Verify`: иначе под нагрузкой отказ различает учётные записи.
- **Argon2id считается в слотах, очередь ограничена.** Одновременно — не больше
  `max(1, GOMAXPROCS/Threads)` вычислений на хэшер: наплыв анонимных логинов
  ждёт в очереди, а не множит по `ARGON2_MEMORY_KIB` на запрос. Слот ждут не больше
  восьми вызовов на слот, следующий сразу получает `ErrBusy` — ответ 503 с
  `Retry-After` (`RetryAfter()` ошибки; `httpkit.ErrorMap` ставит заголовок сам, если
  ошибка домена заворачивает `ErrBusy`), а не
  429: перегрузка общая, а не лимит клиента. Попытку guard'а входа, учтённую до
  проверки, на `ErrBusy` возвращает `bruteforcekit.Guard.Refund`. Ожидание в очереди
  ограничено и `ctx` запроса — передавай его, а не `context.Background()`.
- **Стойкость Argon2id растёт со временем, безопасно для старых хэшей.** Параметры
  зашиты в PHC-строку, поэтому поднятие `Argon2idConfig` не ломает проверку ранее
  выданных хэшей. Перевыпуск под новую стойкость — `NeedsRehash` + `Hash` на
  успешном логине, на стороне потребителя.
- **Тип токена — часть контракта.** `SignAccess` пишет `typ=access`,
  `ParseAccess` требует его: токен другой схемы на том же секрете →
  `ErrWrongTokenType`. `Parse` тип не проверяет — используй его только для
  собственных схем токенов.
- **Refresh: окно повтора, остальное — кража.** Повтор использованного refresh-токена
  в окне `SESSION_RETRY_WINDOW` после его ротации, пока сессия жива и его преемник не
  использован, — повтор клиента, чей ответ потерян: новый преемник, прежний погашен, сессия
  жива. Прочий повтор — вне окна, после использования преемника, погашенный преемник —
  отзывает сессию целиком (`session.ErrReused`); параллельные обновления одним токеном
  живым оставляют не больше одного токена, а проигравший гонку повтор отзывает сессию:
  клиент обновляет последовательно. `Rotate` — вне транзакции (`session.ErrInTx`): отзыв при
  повторе не откатывается, а сбой потребителя после ротации покрывает окно.
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
