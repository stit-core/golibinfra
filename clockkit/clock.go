// Package clockkit предоставляет небольшой port для часов и его
// production-реализацию (Real). Тестовые фейки Fixed и Tickable лежат
// рядом, чтобы не плодить отдельный clocktest-пакет.
package clockkit

import "time"

type Clock interface {
	Now() time.Time
}

// Real — production-реализация: time.Now().
type Real struct{}

func (Real) Now() time.Time { return time.Now() }
