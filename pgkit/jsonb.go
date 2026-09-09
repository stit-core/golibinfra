package pgkit

import (
	"encoding/json"
	"fmt"
)

// UnmarshalJSONB разворачивает jsonb-колонку в dst; пустой/NULL вход — не
// ошибка, dst остаётся как есть.
func UnmarshalJSONB(b []byte, dst any) error {
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("pgkit: unmarshal jsonb: %w", err)
	}
	return nil
}
