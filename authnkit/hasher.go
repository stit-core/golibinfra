package authnkit

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// ErrConfig — конфиг кита вне допустимого окна значений.
var ErrConfig = errors.New("authnkit: invalid config")

// ErrInvalidHash — PHC-строка нечитаема, неканонична или её параметры вне окна.
var ErrInvalidHash = errors.New("authnkit: invalid argon2id hash")

// ErrHashParams — параметры PHC-строки вне окна кита; заворачивает
// ErrInvalidHash, поэтому errors.Is по нему тоже истинен.
var ErrHashParams = fmt.Errorf("%w: parameters out of window", ErrInvalidHash)

// Окно параметров Argon2id, принимаемых и при Hash, и при Verify: параметры
// PHC-строки приходят из хранилища и без окна управляли бы CPU/RAM процесса.
const (
	// MinTime — минимум проходов Argon2id.
	MinTime = 1
	// MaxTime — максимум проходов: стоимость Argon2id линейна по t, поэтому
	// верхняя граница обязательна для строк из хранилища.
	MaxTime = 64
	// MinMemoryKiB — минимум памяти на один хэш, КиБ.
	MinMemoryKiB = 8 * 1024
	// MaxMemoryKiB — максимум памяти на один хэш, КиБ; при t > 1 фактический
	// потолок ниже — стоимость ограничена MaxCostKiBPasses.
	MaxMemoryKiB = 1024 * 1024
	// MaxCostKiBPasses — потолок стоимости одного вычисления, КиБ × проходы:
	// каждый параметр по отдельности в окне (t=64 и m=1 ГиБ), а вместе они
	// стоят порядка десяти секунд и гигабайта на один Verify. 786432 —
	// четырёхкратная стоимость дефолтных параметров (t=3, m=64 МиБ).
	MaxCostKiBPasses = 4 * 3 * 64 * 1024
	// MinThreads — минимум lanes Argon2id.
	MinThreads = 1
	// MaxThreads — максимум lanes: выше числа ядер параллелизм не даёт выигрыша.
	MaxThreads = 16
	// MinSaltLen — минимум длины соли, байт.
	MinSaltLen = 8
	// MaxSaltLen — максимум длины соли, байт.
	MaxSaltLen = 64
	// MinKeyLen — минимум длины хэша, байт.
	MinKeyLen = 16
	// MaxKeyLen — максимум длины хэша, байт.
	MaxKeyLen = 128
)

// phcB64 — строгий RawStdEncoding: нестрогий декодер Go принимает мусор в
// хвостовых битах последнего символа, и у одного хэша было бы несколько
// валидных представлений, а неканоническое осталось бы в БД навсегда.
var phcB64 = base64.RawStdEncoding.Strict()

// Hasher — хэш пароля и проверка против сохранённой PHC-строки.
type Hasher interface {
	// Hash — самоописывающая PHC-строка для plain.
	Hash(ctx context.Context, plain string) (string, error)
	// Verify — совпал ли plain с encoded; ErrInvalidHash — encoded не разобрать.
	Verify(ctx context.Context, plain, encoded string) (bool, error)
	// NeedsRehash — параметры encoded отличаются от текущих параметров хэшера.
	NeedsRehash(encoded string) bool
	// VerifyDummy — холостая проверка для пути «пользователь не найден».
	VerifyDummy(ctx context.Context)
}

// Argon2idParams — параметры Argon2id и одновременно окно, в котором
// принимаются параметры PHC-строки из хранилища.
type Argon2idParams struct {
	Time    uint32 `envconfig:"ARGON2_TIME" default:"3"`
	Memory  uint32 `envconfig:"ARGON2_MEMORY_KIB" default:"65536"`
	Threads uint8  `envconfig:"ARGON2_THREADS" default:"4"`
	KeyLen  uint32 `envconfig:"ARGON2_KEYLEN" default:"32"`
	SaltLen uint32 `envconfig:"ARGON2_SALTLEN" default:"16"`
}

// Validate — параметры внутри окна; вне окна → ErrConfig.
func (p Argon2idParams) Validate() error {
	if err := p.check(); err != nil {
		return fmt.Errorf("%w: argon2id: %w", ErrConfig, err)
	}
	return nil
}

func (p Argon2idParams) check() error {
	switch {
	case p.Time < MinTime || p.Time > MaxTime:
		return fmt.Errorf("t must be within %d..%d", MinTime, MaxTime)
	case p.Threads < MinThreads || p.Threads > MaxThreads:
		return fmt.Errorf("p must be within %d..%d", MinThreads, MaxThreads)
	case p.Memory < MinMemoryKiB || p.Memory > MaxMemoryKiB:
		return fmt.Errorf("m must be within %d..%d KiB", MinMemoryKiB, MaxMemoryKiB)
	case uint64(p.Time)*uint64(p.Memory) > MaxCostKiBPasses:
		return fmt.Errorf("t*m must not exceed %d KiB-passes", MaxCostKiBPasses)
	case p.KeyLen < MinKeyLen || p.KeyLen > MaxKeyLen:
		return fmt.Errorf("hash length must be within %d..%d bytes", MinKeyLen, MaxKeyLen)
	case p.SaltLen < MinSaltLen || p.SaltLen > MaxSaltLen:
		return fmt.Errorf("salt must be within %d..%d bytes", MinSaltLen, MaxSaltLen)
	}
	return nil
}

// Argon2idHasher — Hasher поверх argon2.IDKey с самоописывающей PHC-строкой.
type Argon2idHasher struct {
	p     Argon2idParams
	instr *hashInstruments
}

// NewArgon2idHasher — хэшер с параметрами p; параметры вне окна → ErrConfig.
func NewArgon2idHasher(p Argon2idParams) (*Argon2idHasher, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &Argon2idHasher{p: p, instr: newHashInstruments()}, nil
}

// Hash считает Argon2id со случайной солью и кодирует результат в PHC-строку.
func (h *Argon2idHasher) Hash(ctx context.Context, plain string) (string, error) {
	start := time.Now()
	defer func() {
		h.instr.hashDuration.Record(ctx, time.Since(start).Seconds())
	}()

	salt := make([]byte, h.p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("authnkit: salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, h.p.Time, h.p.Memory, h.p.Threads, h.p.KeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.p.Memory, h.p.Time, h.p.Threads,
		phcB64.EncodeToString(salt),
		phcB64.EncodeToString(key),
	), nil
}

// Verify сверяет plain с PHC-строкой encoded: (false, ErrInvalidHash) — encoded
// битый или вне окна параметров; (false, nil) — пароль не совпал.
func (h *Argon2idHasher) Verify(ctx context.Context, plain, encoded string) (bool, error) {
	start := time.Now()
	defer func() {
		h.instr.verifyDuration.Record(ctx, time.Since(start).Seconds())
	}()

	ph, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(plain), ph.salt, ph.params.Time, ph.params.Memory, ph.params.Threads, ph.params.KeyLen)
	return subtle.ConstantTimeCompare(got, ph.hash) == 1, nil
}

// dummy* — фиксированные входы VerifyDummy: значения не важны, важна та же
// стоимость argon2, что у Verify на существующем пользователе.
var (
	dummyPassword = []byte("authnkit-dummy-password")
	dummySalt     = []byte("authnkit-dummy-salt-do-not-store-it-anywhere-0123456789abcdefghij")
)

// VerifyDummy тратит столько же CPU, сколько Verify: вызывай на пути
// «пользователь не найден», иначе время ответа выдаёт наличие учётной записи.
func (h *Argon2idHasher) VerifyDummy(ctx context.Context) {
	start := time.Now()
	defer func() {
		h.instr.verifyDuration.Record(ctx, time.Since(start).Seconds())
	}()

	_ = argon2.IDKey(dummyPassword, dummySalt[:h.p.SaltLen], h.p.Time, h.p.Memory, h.p.Threads, h.p.KeyLen)
}

// NeedsRehash — параметры encoded отличаются от параметров хэшера (или строка
// нечитаема): после успешного Verify пароль стоит перехэшировать.
func (h *Argon2idHasher) NeedsRehash(encoded string) bool {
	ph, err := parsePHC(encoded)
	if err != nil {
		return true
	}
	return ph.params != h.p
}

type phc struct {
	params Argon2idParams
	salt   []byte
	hash   []byte
}

func parsePHC(encoded string) (phc, error) {
	// Каноничность: base64-декодер Go молча пропускает \r\n, а ParseUint —
	// ведущие нули, поэтому одна строка имела бы несколько представлений.
	if strings.ContainsAny(encoded, " \t\r\n\v\f") {
		return phc{}, fmt.Errorf("%w: whitespace in hash", ErrInvalidHash)
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return phc{}, fmt.Errorf("%w: not a $argon2id$ string", ErrInvalidHash)
	}
	version, err := parseField(parts[2], "v=", 32)
	if err != nil {
		return phc{}, fmt.Errorf("%w: version: %w", ErrInvalidHash, err)
	}
	if version != argon2.Version {
		return phc{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidHash, version)
	}

	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return phc{}, fmt.Errorf("%w: params must be m=,t=,p=", ErrInvalidHash)
	}
	memory, err := parseField(fields[0], "m=", 32)
	if err != nil {
		return phc{}, fmt.Errorf("%w: m: %w", ErrInvalidHash, err)
	}
	iterations, err := parseField(fields[1], "t=", 32)
	if err != nil {
		return phc{}, fmt.Errorf("%w: t: %w", ErrInvalidHash, err)
	}
	threads, err := parseField(fields[2], "p=", 8)
	if err != nil {
		return phc{}, fmt.Errorf("%w: p: %w", ErrInvalidHash, err)
	}

	salt, err := phcB64.DecodeString(parts[4])
	if err != nil {
		return phc{}, fmt.Errorf("%w: salt: %w", ErrInvalidHash, err)
	}
	hash, err := phcB64.DecodeString(parts[5])
	if err != nil {
		return phc{}, fmt.Errorf("%w: hash: %w", ErrInvalidHash, err)
	}
	if len(hash) > MaxKeyLen || len(salt) > MaxSaltLen {
		return phc{}, fmt.Errorf("%w: hash or salt too long", ErrInvalidHash)
	}

	p := Argon2idParams{
		Time:    uint32(iterations), //nolint:gosec // ParseUint с bitSize 32
		Memory:  uint32(memory),     //nolint:gosec // ParseUint с bitSize 32
		Threads: uint8(threads),     //nolint:gosec // ParseUint с bitSize 8
		KeyLen:  uint32(len(hash)),  //nolint:gosec // длина ограничена MaxKeyLen выше
		SaltLen: uint32(len(salt)),  //nolint:gosec // длина ограничена MaxSaltLen выше
	}
	// Окно проверяется до argon2.IDKey: иначе t/m из строки задавали бы
	// стоимость вычисления.
	if err := p.check(); err != nil {
		return phc{}, fmt.Errorf("%w: %w", ErrHashParams, err)
	}
	return phc{params: p, salt: salt, hash: hash}, nil
}

func parseField(s, prefix string, bits int) (uint64, error) {
	digits, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return 0, fmt.Errorf("expected %q", prefix)
	}
	v, err := strconv.ParseUint(digits, 10, bits)
	if err != nil {
		return 0, err
	}
	if strconv.FormatUint(v, 10) != digits {
		return 0, fmt.Errorf("%q is not a canonical number", digits)
	}
	return v, nil
}
