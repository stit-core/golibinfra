package authnkit

import (
	"fmt"

	"github.com/pquerna/otp/totp"
)

type TOTPGenerator struct {
	Issuer string
}

func NewTOTP(issuer string) *TOTPGenerator {
	return &TOTPGenerator{Issuer: issuer}
}

func (g *TOTPGenerator) Generate(accountName string) (secret string, otpauthURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      g.Issuer,
		AccountName: accountName,
	})
	if err != nil {
		return "", "", fmt.Errorf("authnkit: totp generate: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

func (g *TOTPGenerator) Verify(secret, code string) bool {
	ok := totp.Validate(code, secret)
	if ok {
		recordTOTPResult("ok")
	} else {
		recordTOTPResult("invalid")
	}
	return ok
}
