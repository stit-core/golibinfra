package oidckit

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCE генерирует пару (verifier, challenge) для Authorization Code + PKCE (S256).
func PKCE() (verifier, challenge string, err error) {
	verifier, err = RandomToken()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// RandomToken — криптослучайный URL-safe токен (state, nonce, PKCE-verifier).
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oidckit: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
