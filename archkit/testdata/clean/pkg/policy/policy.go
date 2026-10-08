// Package policy — решение policy, общее доменам.
package policy

// Decision — разрешено ли действие и почему нет.
type Decision struct {
	IsAllowed bool
	Reason    string
}

// Allow разрешает действие.
func Allow() Decision { return Decision{IsAllowed: true} }

// Deny отказывает с причиной.
func Deny(reason string) Decision { return Decision{Reason: reason} }
