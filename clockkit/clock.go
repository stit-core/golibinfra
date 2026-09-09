// Package clockkit — port часов: текущий момент, прошедшее время и таймеры за
// интерфейсом Clock. Production-реализация Real поверх пакета time, тестовые
// фейки Fixed и Tickable лежат рядом, чтобы не плодить отдельный clocktest.
package clockkit

import "time"

// Clock — источник времени для кода, который иначе звал бы time.Now и
// time.After напрямую: в production приходит Real, в тесте — Fixed или Tickable.
type Clock interface {
	// Now — текущий момент по этим часам.
	Now() time.Time
	// Since — сколько прошло от t по этим часам.
	Since(t time.Time) time.Duration
	// After — канал, в который через d придёт момент срабатывания.
	After(d time.Duration) <-chan time.Time
	// NewTimer — однократный таймер на d, который можно остановить и перезапустить.
	NewTimer(d time.Duration) Timer
}

// Timer — однократный таймер с семантикой time.Timer: C отдаёт канал
// срабатывания, Stop и Reset возвращают «таймер был активен».
type Timer interface {
	// C — канал срабатывания; таймер шлёт в него ровно один момент.
	C() <-chan time.Time
	// Stop снимает таймер; false — он уже сработал или был остановлен.
	Stop() bool
	// Reset переставляет срабатывание на d от текущего момента часов; false —
	// таймер уже сработал или был остановлен.
	Reset(d time.Duration) bool
}

// Real — production-реализация поверх пакета time.
type Real struct{}

// Now возвращает time.Now().
func (Real) Now() time.Time { return time.Now() }

// Since возвращает time.Since(t) — по монотонным часам рантайма.
func (Real) Since(t time.Time) time.Duration { return time.Since(t) }

// After возвращает time.After(d).
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// NewTimer возвращает таймер поверх time.NewTimer(d).
func (Real) NewTimer(d time.Duration) Timer { return realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
