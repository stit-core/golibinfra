package pgkit

import (
	"encoding/json"
	"fmt"
)

// UnmarshalJSONB разворачивает значение jsonb-колонки в dst. Пустой/NULL вход
// (len(b) == 0) — не ошибка: dst остаётся как есть. Ошибка json обёрнута с
// префиксом kit'а для errors.Is/As на стороне потребителя.
func UnmarshalJSONB(b []byte, dst any) error {
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("pgkit: unmarshal jsonb: %w", err)
	}
	return nil
}
