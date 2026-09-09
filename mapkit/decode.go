// Package mapkit — декодеры значений из map[string]any (типичный результат
// json.Unmarshal в untyped map): чтение по ключу с приведением типа и
// нулевым/nil-дефолтом при отсутствии ключа или несовпадении типа.
// Только stdlib, без I/O.
package mapkit

import (
	"encoding/json"
	"math"
)

// 2^63 точно представимо в float64: всё, что не меньше него, в int64 не влезает.
const maxInt64AsFloat = float64(1 << 63)

// String возвращает строку по ключу k либо "" при отсутствии/несовпадении типа.
func String(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// Strings возвращает срез строк по ключу k: принимаются []any (элементы
// не-строкового типа пропускаются) и []string (возвращается копия). Отсутствие
// ключа или не-массив дают nil; массив без строк — пустой не-nil срез.
func Strings(m map[string]any, k string) []string {
	switch raw := m[k].(type) {
	case []string:
		out := make([]string, len(raw))
		copy(out, raw)
		return out
	case []any:
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Float возвращает число по ключу k как float64 либо 0 при
// отсутствии/несовпадении типа. JSON-числа в map[string]any приходят float64
// (json.Unmarshal) или json.Number (Decoder.UseNumber); целые и float-типы
// поддержаны для map-литералов.
func Float(m map[string]any, k string) float64 {
	f, _ := number(m[k])
	return f
}

// Int возвращает целое по ключу k либо 0, если ключа нет, значение не число
// или не представимо в int. Дробная часть усекается в сторону нуля.
func Int(m map[string]any, k string) int {
	v, ok := integer(m[k])
	if !ok || int64(int(v)) != v {
		return 0
	}
	return int(v)
}

// Int64 возвращает целое по ключу k либо 0, если ключа нет, значение не число
// или не представимо в int64. Дробная часть усекается в сторону нуля.
func Int64(m map[string]any, k string) int64 {
	v, _ := integer(m[k])
	return v
}

// Map возвращает вложенный map[string]any по ключу k либо nil.
func Map(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

// IntPtr — указатель на целое по ключу k либо nil, если ключа нет, значение
// null, не число или не представимо в int. 0 в JSON даёт указатель на 0 — так
// «не передано» отличается от «передан ноль».
func IntPtr(m map[string]any, k string) *int {
	v, ok := integer(m[k])
	if !ok || int64(int(v)) != v {
		return nil
	}
	i := int(v)
	return &i
}

// BoolPtr — указатель на bool по ключу k либо nil, если ключа нет, значение
// null или не bool.
func BoolPtr(m map[string]any, k string) *bool {
	if v, ok := m[k].(bool); ok {
		return &v
	}
	return nil
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return fromUint64(uint64(n))
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return fromUint64(n)
	case float32:
		return fromFloat64(float64(n))
	case float64:
		return fromFloat64(n)
	case json.Number:
		// Int64 сначала: строка целого шире float64 по точности.
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return fromFloat64(f)
	}
	return 0, false
}

func fromUint64(n uint64) (int64, bool) {
	if n > math.MaxInt64 {
		return 0, false
	}
	return int64(n), true
}

func fromFloat64(f float64) (int64, bool) {
	if math.IsNaN(f) || f >= maxInt64AsFloat || f < -maxInt64AsFloat {
		return 0, false
	}
	return int64(f), true
}
