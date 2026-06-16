package yandexaikit

import (
	"context"
	"encoding/json"
	"fmt"
)

const completionAsyncPath = "/foundationModels/v1/completionAsync"

// CompletionAsync submit'ит отложенный completion (тариф ≈½ sync). Возвращает
// operation-id; результат — через AwaitCompletion или PollOperation. Тело
// идентично sync completion (research §3.2).
func (c *Client) CompletionAsync(ctx context.Context, req Request) (string, error) {
	if err := req.validateResponseFormat(); err != nil {
		return "", err
	}
	body, model := c.toCompletionRequest(req, false)
	var op Operation
	if err := c.doJSON(ctx, jsonRequest{
		method:   "POST",
		url:      c.cfg.BaseURL + completionAsyncPath,
		body:     body,
		endpoint: "completion-async",
		model:    model,
	}, &op); err != nil {
		return "", err
	}
	return op.ID, nil
}

// AwaitCompletion поллит операцию completionAsync до done и строит Response
// (async-тариф для cost). Усечённую модель в cost определить из payload нельзя —
// modelUri в response не возвращается, поэтому cost считается по defaultModel/
// прайс-таблице через ModelVersion недоступен; здесь cost = 0 если модель
// неизвестна, потребитель пересчитает по своей таблице.
func (c *Client) AwaitCompletion(ctx context.Context, operationID string) (*Response, error) {
	op, err := c.awaitOperation(ctx, operationID)
	if err != nil {
		return nil, err
	}
	var res CompletionResult
	if err := decodeOperationResponse(op, &res); err != nil {
		return nil, err
	}
	// modelUri в async-response отсутствует; cost считаем по defaultModel-ключу.
	resp := c.buildResponse(res, c.cfg.DefaultModel, true)
	c.instr.recordUsage(ctx, c.cfg.DefaultModel, resp.Usage, resp.Cost)
	return resp, nil
}

// decodeOperationResponse распаковывает Operation.Response (payload с @type) в out.
func decodeOperationResponse(op *Operation, out any) error {
	if len(op.Response) == 0 {
		return fmt.Errorf("%w: operation %s done without response payload", ErrOperationFailed, op.ID)
	}
	if err := json.Unmarshal(op.Response, out); err != nil {
		return fmt.Errorf("yandexaikit: decode operation response: %w", err)
	}
	return nil
}
