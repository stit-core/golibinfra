package authnkit

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
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

type Argon2idHasher struct {
	p Argon2idParams
}

func NewArgon2idHasher(p Argon2idParams) *Argon2idHasher {
	return &Argon2idHasher{p: p}
}

func (h *Argon2idHasher) Hash(plain string) (string, error) {
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

func (h *Argon2idHasher) Verify(plain, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("authnkit: invalid argon2id encoding")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("authnkit: parse version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("authnkit: unsupported argon2 version %d", version)
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("authnkit: parse params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("authnkit: decode salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("authnkit: decode hash: %w", err)
	}
	got := argon2.IDKey([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
