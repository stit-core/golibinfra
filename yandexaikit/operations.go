package yandexaikit

import (
	"context"
	"fmt"
	"time"
)

// PollOperation опрашивает одну async-операцию (GET /operations/{id} на
// operation.api.cloud.yandex.net — другой хост!). Один проход без ожидания:
// gateway, проксирующий пер-поллинг клиенту, зовёт это.
func (c *Client) PollOperation(ctx context.Context, operationID string) (*Operation, error) {
	var op Operation
	if err := c.doJSON(ctx, jsonRequest{
		method:   "GET",
		url:      c.cfg.OperationURL + "/operations/" + operationID,
		endpoint: "operation-poll",
		model:    "",
	}, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// awaitOperation поллит операцию до done с экспоненциальным бэкоффом по clockkit,
// затем возвращает payload Response (или ошибку операции). Интервалы спят на
// time.After, но проверка дедлайна — по c.clock (тестируемый Tickable).
func (c *Client) awaitOperation(ctx context.Context, operationID string) (*Operation, error) {
	deadline := time.Time{}
	if c.poll.Timeout > 0 {
		deadline = c.clock.Now().Add(c.poll.Timeout)
	}
	wait := c.poll.Initial

	for {
		op, err := c.PollOperation(ctx, operationID)
		if err != nil {
			return nil, err
		}
		if op.Done {
			if op.Error != nil {
				return nil, fmt.Errorf("%w: grpc %d: %s", ErrOperationFailed, op.Error.Code, op.Error.Message)
			}
			return op, nil
		}

		if !deadline.IsZero() && !c.clock.Now().Before(deadline) {
			return nil, fmt.Errorf("%w: operation %s not done within poll budget", ErrTimeout, operationID)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}

		wait = time.Duration(float64(wait) * c.poll.Multiplier)
		if wait > c.poll.Max {
			wait = c.poll.Max
		}
	}
}
