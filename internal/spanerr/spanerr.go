// Package spanerr пишет ошибку потребителя в спан с текстом под масками логгера кита.
package spanerr

import (
	"fmt"
	"reflect"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// masker — логгер logkit: текст по его правилам масок.
type masker interface{ MaskText(string) string }

// Text — текст ошибки под масками log. Логгер не из logkit своих правил не отдаёт — маски
// дефолтные.
func Text(err error, log logkit.Logger) string {
	m, ok := log.(masker)
	if !ok {
		m, _ = logkit.Nop().(masker)
	}
	return m.MaskText(err.Error())
}

// Record — exception-событие и статус Error спана, как span.RecordError и SetStatus, но с
// текстом под масками log: ошибка обработчика или job'а — текст потребителя, и e-mail или
// токен, скрытые в записи лога, лежали бы рядом в трассе открытыми.
func Record(span trace.Span, err error, log logkit.Logger) {
	text := Text(err, log)
	span.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.type", typeName(err)),
		attribute.String("exception.message", text),
	))
	span.SetStatus(codes.Error, text)
}

// typeName — имя типа ошибки в форме span.RecordError SDK.
func typeName(err error) string {
	t := reflect.TypeOf(err)
	if t.PkgPath() == "" && t.Name() == "" {
		return t.String()
	}
	return fmt.Sprintf("%s.%s", t.PkgPath(), t.Name())
}
