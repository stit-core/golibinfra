// Package paging — страница списка.
package paging

import (
	"strconv"

	"example.org/shop/pkg/policy"
)

// Page — номер страницы с 1 и её размер.
type Page struct{ Number, Size int }

// String — страница в логах и ошибках.
func (p Page) String() string { return strconv.Itoa(p.Number) + "/" + strconv.Itoa(p.Size) }

// Allowed — страницу такого размера можно запросить.
func (p Page) Allowed() policy.Decision {
	if p.Size > 100 {
		return policy.Deny("page too large")
	}
	return policy.Allow()
}
