// Package ptrkit — generic-хелперы для указателей: конструирование nil-on-zero и
// безопасное разыменование. Без зависимостей, без I/O, без аллокаций сверх *T.
package ptrkit

// Ptr возвращает указатель на v либо nil, если v — нулевое значение типа.
// Удобно при заполнении опциональных полей (`*string`/`*int64`), где пустая
// строка или 0 означают «не задано».
func Ptr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// Deref разыменовывает p либо возвращает нулевое значение T, если p == nil.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
