// Package idkit генерирует внутренние идентификаторы. UUID v7 (RFC 9562) —
// time-sortable: старшие биты несут unix-ms, поэтому id монотонно растут во
// времени и дают локальность в B-tree индексах (PK), в отличие от случайного v4.
package idkit

import "github.com/google/uuid"

// NewV7 возвращает новый UUID v7 строкой. Паникует только при отказе системного
// генератора случайности (crypto/rand) — невосстановимое состояние процесса.
func NewV7() string {
	return uuid.Must(uuid.NewV7()).String()
}
