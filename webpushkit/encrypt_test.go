package webpushkit

import (
	"crypto/ecdh"
	"encoding/base64"
	"testing"
)

// Тест-вектор RFC 8291 §5 ("Push Message Encryption Example"): все ключи, соль и
// ожидаемое тело заданы стандартом — сверяем байты результата один-в-один.
const (
	vecPlaintext = "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"
	vecAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	vecUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	vecASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	vecSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	vecExpected  = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

func TestEncryptPayload_RFC8291Vector(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, vecASPrivate))
	if err != nil {
		t.Fatalf("as_private: %v", err)
	}

	body, err := encryptPayload(
		mustB64(t, vecPlaintext),
		mustB64(t, vecUAPublic),
		mustB64(t, vecAuth),
		asPriv,
		mustB64(t, vecSalt),
	)
	if err != nil {
		t.Fatalf("encryptPayload: %v", err)
	}

	if got := base64.RawURLEncoding.EncodeToString(body); got != vecExpected {
		t.Fatalf("body mismatch\n got: %s\nwant: %s", got, vecExpected)
	}
}
