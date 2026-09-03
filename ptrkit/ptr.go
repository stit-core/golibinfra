// Package ptrkit — generic-хелперы для указателей: Ptr (nil на нулевом
// значении), Of (всегда указатель) и Deref (безопасное разыменование).
// Без зависимостей и I/O.
package ptrkit

// Ptr возвращает указатель на v либо nil, если v — нулевое значение типа
// ("", 0, false, …): для опциональных полей, где пустое значение означает
// «не задано». Нужен указатель и на нулевое значение — Of.
func Ptr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// Of возвращает указатель на v всегда, включая нулевое значение.
func Of[T any](v T) *T { return &v }

// Deref разыменовывает p либо возвращает нулевое значение T, если p == nil.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
