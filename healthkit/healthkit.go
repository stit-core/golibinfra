// Package healthkit — именованная health-проба для fx-группы `healthkit.probes`:
// киты (pgkit, natskit, cachekit, storagekit) возвращают Probe, httpkit
// собирает группу и отвечает по ней на /ready.
package healthkit

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"time"

	"go.uber.org/fx"
)

// Group — имя fx-группы, из которой httpkit читает пробы.
const Group = "healthkit.probes"

// Probe — именованная проверка готовности зависимости. Name — идентификатор
// в логе /ready («pgkit», «natskit/events»); структура, а не голая функция,
// потому что без имени упавшую зависимость в логе не отличить.
type Probe struct {
	Name  string
	Check func(ctx context.Context) error
}

// New собирает Probe из имени и проверки.
func New(name string, check func(ctx context.Context) error) Probe {
	return Probe{Name: name, Check: check}
}

// PanicError — проверка паниковала: Run возвращает её вместо падения процесса.
// Value — значение panic, Stack — стек горутины проверки.
type PanicError struct {
	Probe string
	Value any
	Stack []byte
}

// Error — «healthkit: probe "<имя>" panicked: <значение panic>».
func (e *PanicError) Error() string {
	return fmt.Sprintf("healthkit: probe %q panicked: %v", e.Probe, e.Value)
}

// Run выполняет проверку. Probe без Check — ошибка конфигурации графа, а не
// паника в обработчике /ready; panic проверки — *PanicError: readiness зовёт
// пробы в своих горутинах, и panic там уронил бы процесс. Ошибку проверки
// возвращает как есть: кит префиксует её своим именем, идентичность пробы несёт Name.
func (p Probe) Run(ctx context.Context) (err error) {
	if p.Check == nil {
		return fmt.Errorf("healthkit: probe %q: check is nil", p.Name)
	}
	defer func() {
		if rec := recover(); rec != nil {
			err = &PanicError{Probe: p.Name, Value: rec, Stack: debug.Stack()}
		}
	}()
	return p.Check(ctx)
}

// WithTimeout ограничивает время одной проверки: зависшая зависимость не должна
// держать /ready дольше, чем оркестратор готов ждать. d <= 0 — проба без
// изменений. Panic проверки — *PanicError, как у Run.
func WithTimeout(p Probe, d time.Duration) Probe {
	if d <= 0 || p.Check == nil {
		return p
	}
	return Probe{Name: p.Name, Check: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()

		// Буфер: проба, игнорирующая ctx, завершится позже и не заблокируется.
		done := make(chan error, 1)
		go func() { done <- p.Run(ctx) }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return fmt.Errorf("healthkit: probe %q timed out after %s: %w", p.Name, d, ctx.Err())
		}
	}}
}

// Provide регистрирует конструктор, возвращающий Probe, в группу `healthkit.probes`.
// Конструктор неверного типа отвергается при сборке графа: иначе значение
// молча ложится в группу другим типом и /ready остаётся «ok» без проб.
// Дополнительные аннотации (например fx.ParamTags для именованного инстанса)
// применяются до групповой; свой fx.ResultTags передавать нельзя.
func Provide(ctor any, anns ...fx.Annotation) fx.Option {
	if err := validateCtor(ctor); err != nil {
		return fx.Error(err)
	}
	return fx.Provide(fx.Annotate(ctor, append(anns, fx.ResultTags(`group:"`+Group+`"`))...))
}

var probeType = reflect.TypeFor[Probe]()

func validateCtor(ctor any) error {
	t := reflect.TypeOf(ctor)
	if t == nil || t.Kind() != reflect.Func {
		return fmt.Errorf("healthkit: Provide expects a constructor function returning healthkit.Probe, got %T", ctor)
	}
	if t.NumOut() == 0 || t.Out(0) != probeType {
		return fmt.Errorf("healthkit: Provide constructor must return healthkit.Probe as its first result, got %v", t)
	}
	return nil
}
