// Package stopbudget — доля бюджета остановки fx для хука OnStop, который ждёт: две трети остатка
// — ему, треть — хукам после него.
package stopbudget

import (
	"context"
	"time"
)

// Share — ctx на две трети остатка дедлайна stop-ctx. fx проверяет ctx перед каждым OnStop и на
// истёкшем пропускает остальные: хук, прождавший до дедлайна, отнял бы остановку у всех после
// себя (закрытие шины и пула, финальный экспорт telemetry). Без дедлайна — ctx с отменой.
func Share(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Until(deadline)*2/3)
}
