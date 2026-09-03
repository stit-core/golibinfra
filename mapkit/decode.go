// Package mapkit — декодеры значений из map[string]any (типичный результат
// json.Unmarshal в untyped map): чтение по ключу с приведением типа и
// нулевым/nil-дефолтом при отсутствии ключа или несовпадении типа. Без I/O.
package mapkit

// String возвращает строку по ключу k либо "" при отсутствии/несовпадении типа.
func String(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// Strings возвращает срез строк по ключу k; элементы не-строкового типа
// пропускаются, отсутствие ключа или не-массив дают nil.
func Strings(m map[string]any, k string) []string {
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Float возвращает число по ключу k как float64. JSON-числа в map[string]any
// приходят float64 (json.Unmarshal); int/int64 поддержаны для map-литералов.
func Float(m map[string]any, k string) float64 {
	f, _ := number(m[k])
	return f
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// Int возвращает Float(m, k), усечённое до int.
func Int(m map[string]any, k string) int { return int(Float(m, k)) }

// Int64 возвращает Float(m, k), усечённое до int64.
func Int64(m map[string]any, k string) int64 { return int64(Float(m, k)) }

// Map возвращает вложенный map[string]any по ключу k либо nil.
func Map(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

// IntPtr — указатель на число по ключу k либо nil, если ключа нет, значение
// null или не число. 0 в JSON даёт указатель на 0 — так «не передано»
// отличается от «передан ноль».
func IntPtr(m map[string]any, k string) *int {
	f, ok := number(m[k])
	if !ok {
		return nil
	}
	v := int(f)
	return &v
}

// BoolPtr — указатель на bool по ключу k либо nil, если ключа нет, значение
// null или не bool.
func BoolPtr(m map[string]any, k string) *bool {
	if v, ok := m[k].(bool); ok {
		return &v
	}
	return nil
}
