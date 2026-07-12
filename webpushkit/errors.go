package webpushkit

import (
	"errors"
	"fmt"
)

var (
	// ErrConfig — VAPID-конфиг невалиден (нет/битый приватный ключ, subject не
	// mailto:/https:, public не соответствует private).
	ErrConfig = errors.New("webpushkit: invalid config")

	// ErrSubscription — подписка синтаксически невалидна (битый p256dh/auth,
	// неверная длина). В отличие от ErrSubscriptionGone это ошибка входных данных,
	// а не сигнал «удали подписку».
	ErrSubscription = errors.New("webpushkit: invalid subscription")

	// ErrSubscriptionGone — push-сервис вернул 404/410: подписка больше не
	// действительна, потребитель должен удалить её из своего хранилища.
	ErrSubscriptionGone = errors.New("webpushkit: subscription gone")

	// ErrPayloadTooLarge — plaintext превышает потолок, при котором зашифрованное
	// тело укладывается в гарантированные push-сервисами 4096 байт (либо сам
	// push-сервис ответил 413).
	ErrPayloadTooLarge = errors.New("webpushkit: payload too large")

	// ErrPush — прочий не-2xx ответ push-сервиса; конкретный статус несёт *PushError.
	ErrPush = errors.New("webpushkit: push service error")
)

// PushError — не-2xx ответ push-сервиса, не подпадающий под gone/too-large.
// Оборачивает ErrPush (errors.Is(err, ErrPush)); StatusCode/RetryAfter позволяют
// потребителю решить про backoff (например, 429 + Retry-After).
type PushError struct {
	StatusCode int
	RetryAfter string
	Body       string
}

func (e *PushError) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("webpushkit: push service returned %d (retry-after %s)", e.StatusCode, e.RetryAfter)
	}
	return fmt.Sprintf("webpushkit: push service returned %d", e.StatusCode)
}

func (e *PushError) Unwrap() error { return ErrPush }
