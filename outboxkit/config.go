package outboxkit

import (
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// PublishMode задаёт компромисс «порядок vs пропускная способность» на хопе
// worker→брокер: batch — async-батч (один round-trip на тик), ordered —
// последовательная публикация со стопом на первой ошибке.
type PublishMode string

const (
	// PublishBatch — async-батч, порядок персиста best-effort.
	PublishBatch PublishMode = "batch"
	// PublishOrdered — последовательная публикация, строгий порядок очереди.
	PublishOrdered PublishMode = "ordered"
)

// DefaultMaxPayloadBytes — предел JSON-тела события у NewBus (дефолт OUTBOX_MAX_PAYLOAD_BYTES):
// половина max_payload NATS (1 MiB) — запас под заголовки и пробелы представления хранилища
// (JSONB pgstore), но не под числа в экспоненте: их JSONB выписывает целиком.
const DefaultMaxPayloadBytes = 512 << 10

// Config — конфиг orchestration-слоя (Bus + Worker); читается из ENV
// через NewConfig, провайдится caller'ом.
type Config struct {
	// BatchSize — сколько строк воркер вычитывает за тик (LIMIT).
	BatchSize int `envconfig:"OUTBOX_BATCH_SIZE"   default:"100"`
	// Interval — период тика воркера.
	Interval time.Duration `envconfig:"OUTBOX_INTERVAL"     default:"1s"`
	// PublishMode — режим публикации: batch или ordered.
	PublishMode PublishMode `envconfig:"OUTBOX_PUBLISH_MODE" default:"batch"`
	// MaxAttempts — после стольких неудачных публикаций строка помечается dead; сбой брокера
	// целиком (ErrUnavailable) попыткой не считается. >= 1 или -1 — без предела: poison держит
	// ordered-очередь, пока причину не починят.
	MaxAttempts int `envconfig:"OUTBOX_MAX_ATTEMPTS" default:"25"`
	// TickTimeout — дедлайн одного тика (транзакция + публикация); > 0.
	TickTimeout time.Duration `envconfig:"OUTBOX_TICK_TIMEOUT" default:"30s"`
	// RetryBackoff — пауза перед первым ретраем строки; каждая следующая
	// неудача удваивает её до RetryMaxBackoff. 0 — ретрай на ближайшем тике.
	RetryBackoff time.Duration `envconfig:"OUTBOX_RETRY_BACKOFF" default:"1s"`
	// RetryMaxBackoff — потолок паузы между попытками; 0 — без потолка.
	RetryMaxBackoff time.Duration `envconfig:"OUTBOX_RETRY_MAX_BACKOFF" default:"5m"`
	// MaxPayloadBytes — предел JSON-тела события: больше — ErrPayloadTooLarge из Publish в
	// транзакции домена, а не строка, которую брокер не примет никогда. >= 1.
	MaxPayloadBytes int `envconfig:"OUTBOX_MAX_PAYLOAD_BYTES" default:"524288"`
}

// NewConfig читает Config из ENV (`OUTBOX_*`) и валидирует его.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// Validate проверяет инварианты конфига; нарушение — ErrConfig.
func (c Config) Validate() error {
	switch c.PublishMode {
	case PublishBatch, PublishOrdered:
	default:
		return fmt.Errorf("%w: OUTBOX_PUBLISH_MODE must be batch|ordered, got %q", ErrConfig, c.PublishMode)
	}
	switch {
	case c.BatchSize < 1:
		return fmt.Errorf("%w: OUTBOX_BATCH_SIZE must be >= 1, got %d", ErrConfig, c.BatchSize)
	case c.Interval <= 0:
		return fmt.Errorf("%w: OUTBOX_INTERVAL must be > 0, got %s", ErrConfig, c.Interval)
	case c.MaxAttempts == 0 || c.MaxAttempts < -1:
		return fmt.Errorf("%w: OUTBOX_MAX_ATTEMPTS must be >= 1 or -1 (unlimited), got %d", ErrConfig, c.MaxAttempts)
	case c.MaxPayloadBytes < 1:
		return fmt.Errorf("%w: OUTBOX_MAX_PAYLOAD_BYTES must be >= 1, got %d", ErrConfig, c.MaxPayloadBytes)
	case c.TickTimeout <= 0:
		return fmt.Errorf("%w: OUTBOX_TICK_TIMEOUT must be > 0, got %s", ErrConfig, c.TickTimeout)
	case c.RetryBackoff < 0:
		return fmt.Errorf("%w: OUTBOX_RETRY_BACKOFF must be >= 0, got %s", ErrConfig, c.RetryBackoff)
	case c.RetryMaxBackoff < 0:
		return fmt.Errorf("%w: OUTBOX_RETRY_MAX_BACKOFF must be >= 0, got %s", ErrConfig, c.RetryMaxBackoff)
	case c.RetryMaxBackoff > 0 && c.RetryMaxBackoff < c.RetryBackoff:
		return fmt.Errorf("%w: OUTBOX_RETRY_MAX_BACKOFF (%s) must be >= OUTBOX_RETRY_BACKOFF (%s)",
			ErrConfig, c.RetryMaxBackoff, c.RetryBackoff)
	}
	return nil
}

// checkIdleInTx — тик дольше idle_in_transaction_session_timeout пула: пока воркер ждёт PubAck,
// транзакция простаивает, и Postgres оборвал бы сессию раньше конца тика. Ноль таймаута пула —
// без лимита.
func checkIdleInTx(cfg Config, pool pgkit.PoolConfig) error {
	if pool.IdleInTxSessionTimeout > 0 && cfg.TickTimeout >= pool.IdleInTxSessionTimeout {
		return fmt.Errorf("%w: OUTBOX_TICK_TIMEOUT (%s) must be < PG_IDLE_IN_TX_SESSION_TIMEOUT (%s)",
			ErrConfig, cfg.TickTimeout, pool.IdleInTxSessionTimeout)
	}
	return nil
}
