package authnkit

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// tokenBytes — энтропия одноразового токена: 256 бит.
const tokenBytes = 32

// NewToken — одноразовый непрозрачный токен (ссылка сброса пароля, подтверждение
// email, refresh-сессия): 256 бит из crypto/rand в hex, 64 символа. Хранится не
// токен, а HashToken; hex-цепочку такой длины logkit маскирует по умолчанию.
func NewToken() string {
	b := make([]byte, tokenBytes)
	// crypto/rand.Read не возвращает ошибку: сбой источника ОС роняет процесс.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HashToken — SHA-256 токена в hex для хранения и поиска по индексу. Медленный хеш
// (Argon2id) не нужен: у токена полная энтропия, перебор по хешу бесполезен.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
