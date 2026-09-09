// Package idkit генерирует и проверяет внутренние идентификаторы. UUID v7
// (RFC 9562) — time-sortable: старшие биты несут unix-ms, поэтому id монотонно
// растут во времени и дают локальность в B-tree индексах (PK).
package idkit

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrInvalid — строка не является каноническим UUID.
var ErrInvalid = errors.New("idkit: invalid uuid")

// NewV7 возвращает новый UUID v7 строкой. Паникует только при отказе системного
// генератора случайности (crypto/rand) — невосстановимое состояние процесса.
func NewV7() string {
	return uuid.Must(uuid.NewV7()).String()
}

// Validate принимает только канонический вид: 36 символов, lowercase hex,
// 8-4-4-4-12 через дефисы. Форматы, которые допускает uuid.Parse (URN,
// фигурные скобки, 32 hex без дефисов, верхний регистр), — ErrInvalid.
func Validate(s string) error {
	_, err := parseCanonical(s)
	return err
}

// IsV7 сообщает, что s — канонический UUID версии 7 с вариантом RFC 4122,
// то есть мог быть выдан NewV7.
func IsV7(s string) bool {
	u, err := parseCanonical(s)
	return err == nil && u.Version() == 7 && u.Variant() == uuid.RFC4122
}

func parseCanonical(s string) (uuid.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil || u.String() != s {
		return uuid.Nil, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	return u, nil
}
