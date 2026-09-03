package authnkit

import (
	"fmt"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

const totpPeriodSeconds = 30

type TOTPConfig struct {
	Issuer string `envconfig:"TOTP_ISSUER" required:"true"`
	// Skew — сколько соседних 30-секундных окон принимается с каждой стороны:
	// 1 → код живёт ~90 с, 0 → только текущее окно.
	Skew uint `envconfig:"TOTP_SKEW" default:"1"`
}

type TOTPGenerator struct {
	issuer string
	skew   uint
	clock  clockkit.Clock
	instr  *instruments
}

func NewTOTP(cfg TOTPConfig, opts ...Option) *TOTPGenerator {
	o := applyOptions(opts)
	return &TOTPGenerator{issuer: cfg.Issuer, skew: cfg.Skew, clock: o.clock, instr: newInstruments()}
}

func (g *TOTPGenerator) Generate(accountName string) (secret, otpauthURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      g.issuer,
		AccountName: accountName,
	})
	if err != nil {
		return "", "", fmt.Errorf("authnkit: totp generate: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// Verify не защищает от повторного ввода того же кода в пределах окна:
// однократность enforce'ит потребитель (см. docs/usage.md).
func (g *TOTPGenerator) Verify(secret, code string) bool {
	ok, err := totp.ValidateCustom(code, secret, g.clock.Now().UTC(), totp.ValidateOpts{
		Period:    totpPeriodSeconds,
		Skew:      g.skew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil || !ok {
		g.instr.recordTOTP("invalid")
		return false
	}
	g.instr.recordTOTP("ok")
	return true
}
