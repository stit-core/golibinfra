// Package authnkit — кирпичики аутентификации: Hasher (Argon2id), HS256-JWT
// поверх go-jose, TOTP (RFC 6238) и экземплярный RBAC-реестр. Auth-flow'ы
// (login, refresh, MFA enrollment) — за consumer'ом. Эмитит
// jwt_verifications_total{result}, password_{hash,verify}_duration_seconds,
// totp_verifications_total{result}.
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

var (
	ErrConfig      = errors.New("authnkit: invalid config")
	ErrInvalidHash = errors.New("authnkit: invalid argon2id hash")
)

// Окно параметров Argon2id, принимаемых и при Hash, и при Verify: параметры
// PHC-строки приходят из хранилища и без окна управляли бы CPU/RAM процесса.
const (
	MinMemoryKiB = 8 * 1024
	MaxMemoryKiB = 1024 * 1024
	MinSaltLen   = 8
	MaxSaltLen   = 64
	MinKeyLen    = 16
	MaxKeyLen    = 128
)

type Hasher interface {
	Hash(plain string) (string, error)
	Verify(plain, encoded string) (bool, error)
}

type Argon2idParams struct {
	Time    uint32 `envconfig:"ARGON2_TIME" default:"3"`
	Memory  uint32 `envconfig:"ARGON2_MEMORY_KIB" default:"65536"`
	Threads uint8  `envconfig:"ARGON2_THREADS" default:"4"`
	KeyLen  uint32 `envconfig:"ARGON2_KEYLEN" default:"32"`
	SaltLen uint32 `envconfig:"ARGON2_SALTLEN" default:"16"`
}

func (p Argon2idParams) check() error {
	switch {
	case p.Time < 1:
		return errors.New("t must be >= 1")
	case p.Threads < 1:
		return errors.New("p must be >= 1")
	case p.Memory < MinMemoryKiB || p.Memory > MaxMemoryKiB:
		return fmt.Errorf("m must be within %d..%d KiB", MinMemoryKiB, MaxMemoryKiB)
	case p.KeyLen < MinKeyLen || p.KeyLen > MaxKeyLen:
		return fmt.Errorf("hash length must be within %d..%d bytes", MinKeyLen, MaxKeyLen)
	case p.SaltLen < MinSaltLen || p.SaltLen > MaxSaltLen:
		return fmt.Errorf("salt must be within %d..%d bytes", MinSaltLen, MaxSaltLen)
	}
	return nil
}

type Argon2idHasher struct {
	p     Argon2idParams
	instr *instruments
}

func NewArgon2idHasher(p Argon2idParams) *Argon2idHasher {
	return &Argon2idHasher{p: p, instr: newInstruments()}
}

func (h *Argon2idHasher) Hash(plain string) (string, error) {
	start := time.Now()
	defer func() {
		h.instr.hashDuration.Record(context.Background(), time.Since(start).Seconds())
	}()

	if err := h.p.check(); err != nil {
		return "", fmt.Errorf("%w: argon2id: %w", ErrConfig, err)
	}
	salt := make([]byte, h.p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("authnkit: salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, h.p.Time, h.p.Memory, h.p.Threads, h.p.KeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.p.Memory, h.p.Time, h.p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify: (false, ErrInvalidHash) — encoded битый или вне окна параметров;
// (false, nil) — пароль не совпал.
func (h *Argon2idHasher) Verify(plain, encoded string) (bool, error) {
	start := time.Now()
	defer func() {
		h.instr.verifyDuration.Record(context.Background(), time.Since(start).Seconds())
	}()

	ph, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(plain), ph.salt, ph.params.Time, ph.params.Memory, ph.params.Threads, ph.params.KeyLen)
	return subtle.ConstantTimeCompare(got, ph.hash) == 1, nil
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

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return phc{}, fmt.Errorf("%w: salt: %w", ErrInvalidHash, err)
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
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
	if err := p.check(); err != nil {
		return phc{}, fmt.Errorf("%w: %w", ErrInvalidHash, err)
	}
	return phc{params: p, salt: salt, hash: hash}, nil
}

func parseField(s, prefix string, bits int) (uint64, error) {
	v, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return 0, fmt.Errorf("expected %q", prefix)
	}
	return strconv.ParseUint(v, 10, bits)
}
