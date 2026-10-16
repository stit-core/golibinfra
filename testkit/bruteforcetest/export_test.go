package bruteforcetest

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
)

// RefundBelowZero — Counting, у которого возврат уводит счётчик окна ниже нуля: возврат без учтённой в окне попытки
// оставляет запас на попытку сверх порога.
func RefundBelowZero(c *Counting) bruteforcekit.Guard { return sinkingCounting{c} }

type sinkingCounting struct{ *Counting }

func (c sinkingCounting) Refund(ctx context.Context, subject, ip string) error {
	key, err := c.key(subject, ip)
	if err != nil {
		return err
	}
	c.mu.Lock()
	var empty []*countingRow
	for _, k := range []Attempt{{Subject: key.Subject}, key} {
		if row := c.rows[k]; row != nil && row.failedCount == 0 {
			empty = append(empty, row)
		}
	}
	c.mu.Unlock()
	if err := c.Counting.Refund(ctx, subject, ip); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, row := range empty {
		row.failedCount--
	}
	return nil
}
