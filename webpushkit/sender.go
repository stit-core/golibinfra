// Package webpushkit — транспорт доставки Web Push: VAPID-аутентификация
// (RFC 8292) и aes128gcm-шифрование payload (RFC 8291 поверх RFC 8188), POST на
// endpoint push-сервиса (RFC 8030). Kit — только типы и отправка; хранение
// подписок и планирование — зона потребителя (notification-домен).
package webpushkit

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Urgency — приоритет доставки (RFC 8030 §5.3): push-сервис может придержать
// low-urgency сообщения, экономя батарею устройства.
type Urgency string

const (
	UrgencyVeryLow Urgency = "very-low"
	UrgencyLow     Urgency = "low"
	UrgencyNormal  Urgency = "normal"
	UrgencyHigh    Urgency = "high"
)

// Sender отправляет Web Push сообщения от лица одной VAPID-идентичности.
// Потокобезопасен: не хранит состояния между вызовами Send.
type Sender struct {
	signer     *vapidSigner
	client     *http.Client
	defaultTTL time.Duration
}

// NewSender готовит отправителя из VAPID-конфига. client опционален: nil →
// http.DefaultClient (потребитель может передать httpclient/otelhttp-обёртку,
// чтобы получить транспортные RED-метрики). Невалидный конфиг → ErrConfig.
func NewSender(cfg Config, client *http.Client) (*Sender, error) {
	signer, err := newVAPIDSigner(cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Sender{signer: signer, client: client, defaultTTL: cfg.DefaultTTL}, nil
}

type sendOptions struct {
	ttl     time.Duration
	urgency Urgency
	topic   string
}

// SendOption переопределяет параметры одной доставки.
type SendOption func(*sendOptions)

// WithTTL задаёт TTL сообщения (сколько push-сервис держит его для оффлайн-клиента).
func WithTTL(ttl time.Duration) SendOption { return func(o *sendOptions) { o.ttl = ttl } }

// WithUrgency задаёт приоритет доставки (заголовок Urgency).
func WithUrgency(u Urgency) SendOption { return func(o *sendOptions) { o.urgency = u } }

// WithTopic задаёт Topic: новое сообщение с тем же топиком вытесняет прежнее
// недоставленное (полезно, чтобы не копить устаревшие уведомления).
func WithTopic(topic string) SendOption { return func(o *sendOptions) { o.topic = topic } }

// Send шифрует payload под ключи подписки и POST'ит его на endpoint. Возвращает:
// nil при 2xx; ErrSubscriptionGone при 404/410 (потребитель удаляет подписку);
// ErrPayloadTooLarge; *PushError (Is ErrPush) при прочих не-2xx.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, opts ...SendOption) error {
	if len(payload) > maxPayloadBytes {
		return ErrPayloadTooLarge
	}
	uaPublic, authSecret, err := sub.keys()
	if err != nil {
		return err
	}

	// Эфемерная пара сервера приложения и record salt — новые на каждое сообщение
	// (RFC 8291): переиспользование ключа/соли компрометирует шифрование.
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return err
	}

	body, err := encryptPayload(payload, uaPublic, authSecret, asPriv, salt)
	if err != nil {
		return err
	}

	o := sendOptions{ttl: s.defaultTTL}
	for _, opt := range opts {
		opt(&o)
	}

	authz, err := s.signer.authHeader(sub.Endpoint, time.Now())
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authz)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(o.ttl.Seconds())))
	if o.urgency != "" {
		req.Header.Set("Urgency", string(o.urgency))
	}
	if o.topic != "" {
		req.Header.Set("Topic", o.topic)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return classify(resp)
}

func classify(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrSubscriptionGone
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return ErrPayloadTooLarge
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &PushError{
			StatusCode: resp.StatusCode,
			RetryAfter: resp.Header.Get("Retry-After"),
			Body:       string(body),
		}
	}
}
