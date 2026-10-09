package authnkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

const (
	// TOTPPeriod — длина окна TOTP (RFC 6238 §4, значение по умолчанию у
	// authenticator-приложений).
	TOTPPeriod = 30 * time.Second
	// MaxTOTPSkew — максимум соседних окон с каждой стороны: каждое окно
	// умножает число кодов, подходящих к одной попытке.
	MaxTOTPSkew = 2
)

// TOTPConfig — issuer в otpauth-URL и окно приёма кода.
type TOTPConfig struct {
	Issuer string `envconfig:"TOTP_ISSUER" required:"true"`
	// Skew — сколько соседних 30-секундных окон принимается с каждой стороны:
	// 1 → код живёт ~90 с, 0 → только текущее окно.
	Skew uint `envconfig:"TOTP_SKEW" default:"1"`
}

// Validate требует непустой Issuer без ':' и '/' (иначе label otpauth-URL
// разбирается неоднозначно) и Skew не больше MaxTOTPSkew; иначе ErrConfig.
func (c TOTPConfig) Validate() error {
	switch {
	case c.Issuer == "":
		return fmt.Errorf("%w: TOTP_ISSUER is required", ErrConfig)
	case strings.ContainsAny(c.Issuer, ":/"):
		return fmt.Errorf("%w: TOTP_ISSUER must not contain ':' or '/'", ErrConfig)
	case c.Skew > MaxTOTPSkew:
		return fmt.Errorf("%w: TOTP_SKEW must be within 0..%d, got %d", ErrConfig, MaxTOTPSkew, c.Skew)
	}
	return nil
}

// TOTPGenerator — enrollment и проверка кодов TOTP (RFC 6238, SHA1, 6 цифр).
type TOTPGenerator struct {
	issuer string
	skew   uint
	clock  clockkit.Clock
	instr  *totpInstruments
}

// NewTOTP — генератор по cfg; невалидный конфиг → ErrConfig.
func NewTOTP(cfg TOTPConfig, opts ...TOTPOption) (*TOTPGenerator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	o := totpOptions(opts)
	return &TOTPGenerator{issuer: cfg.Issuer, skew: cfg.Skew, clock: o.clock, instr: newTOTPInstruments()}, nil
}

// Generate возвращает новый секрет (base32) и otpauth-URL для QR-кода.
// accountName с ':' или '/' → ошибка: label URL стал бы неоднозначным.
func (g *TOTPGenerator) Generate(accountName string) (secret, otpauthURL string, err error) {
	if accountName == "" || strings.ContainsAny(accountName, ":/") {
		return "", "", fmt.Errorf("%w: TOTP account name must be non-empty and free of ':' and '/'", ErrConfig)
	}
	// Параметры заданы явно: Verify считает по ним же, а не по дефолтам библиотеки.
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      g.issuer,
		AccountName: accountName,
		Period:      uint(TOTPPeriod / time.Second),
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return "", "", fmt.Errorf("authnkit: totp generate: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// Verify проверяет код в окне ±Skew. Нечитаемый секрет → ошибка (не «код
// неверен»). От повторного ввода того же кода не защищает — см. VerifyOnce.
func (g *TOTPGenerator) Verify(ctx context.Context, secret, code string) (bool, error) {
	result, ok, err := g.validate(secret, code)
	g.instr.recordTOTP(ctx, result)
	return ok, err
}

// validate — код в окне ±Skew, без метрики: её пишут Verify и VerifyOnce одной записью итога.
func (g *TOTPGenerator) validate(secret, code string) (result string, ok bool, err error) {
	valid, err := totp.ValidateCustom(code, secret, g.clock.Now().UTC(), totp.ValidateOpts{
		Period:    uint(TOTPPeriod / time.Second),
		Skew:      g.skew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	switch {
	case err != nil:
		return "secret_invalid", false, fmt.Errorf("authnkit: totp verify: %w", err)
	case !valid:
		return "invalid", false, nil
	}
	return "ok", true, nil
}

// UsedCodeStore — однократность принятого TOTP-кода: MarkUsed возвращает false,
// если ключ уже отмечен и ещё не истёк. Реализации: MemoryUsedCodeStore
// (single-replica) или обёртка над cachekit.Cache.SetNX (несколько реплик).
type UsedCodeStore interface {
	// MarkUsed — true, если ключ отмечен впервые (код ещё не использовали).
	MarkUsed(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// VerifyOnce — Verify плюс защита от replay: код, уже принятый в пределах
// своего окна, отвергается (false, nil). Ошибка store → (false, error):
// решение «пускать или нет» при недоступном хранилище остаётся за потребителем.
func (g *TOTPGenerator) VerifyOnce(ctx context.Context, secret, code string, store UsedCodeStore) (bool, error) {
	result, ok, err := g.validate(secret, code)
	if ok {
		result, ok, err = g.markUsed(ctx, secret, code, store)
	}
	g.instr.recordTOTP(ctx, result)
	return ok, err
}

// markUsed — однократность принятого кода; повтор в своём окне — replay.
func (g *TOTPGenerator) markUsed(ctx context.Context, secret, code string, store UsedCodeStore) (result string, ok bool, err error) {
	fresh, err := store.MarkUsed(ctx, usedCodeKey(secret, code), g.codeLifetime())
	switch {
	case err != nil:
		return "store_error", false, fmt.Errorf("authnkit: totp used-code store: %w", err)
	case !fresh:
		return "replay", false, nil
	}
	return "ok", true, nil
}

// codeLifetime — сколько код остаётся валидным: своё окно плюс ±Skew соседних.
func (g *TOTPGenerator) codeLifetime() time.Duration {
	return time.Duration(2*g.skew+1) * TOTPPeriod //nolint:gosec // skew ≤ MaxTOTPSkew (TOTPConfig.Validate)
}

// usedCodeKey — секрет в ключ не попадает: хранилище может быть общим (Redis).
func usedCodeKey(secret, code string) string {
	sum := sha256.Sum256([]byte(secret + "\x00" + code))
	return "authnkit:totp:" + hex.EncodeToString(sum[:])
}
