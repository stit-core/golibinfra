package amocrmkit

import (
	"strings"
	"testing"
	"time"
)

func TestMD5Hex_KnownVectors(t *testing.T) {
	cases := map[string]string{
		"":    "d41d8cd98f00b204e9800998ecf8427e", // MD5 пустой строки (GET-тело), подтверждён докой
		"abc": "900150983cd24fb0d6963f7d28e17f72", // канонический MD5("abc")
	}
	for in, want := range cases {
		if got := md5Hex([]byte(in)); got != want {
			t.Errorf("md5Hex(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestHMACSHA1Hex_RFC2202(t *testing.T) {
	// RFC 2202, test case 2: key="Jefe".
	got := hmacSHA1Hex([]byte("what do ya want for nothing?"), "Jefe")
	const want = "effcdf6ae5eb2fa2d27416d5f184df9c259a7c79"
	if got != want {
		t.Fatalf("hmacSHA1Hex = %s, want %s", got, want)
	}
}

func TestSignRequest_AssemblyAndDate(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"a":1}`)
	path := "/v2/origin/custom/f90ba33d/connect"

	sh := signRequest(now, "post", path, body, "secret") // метод в нижнем — должен апперкейзиться

	if sh.Date != "Mon, 01 Jan 2024 12:00:00 +0000" {
		t.Errorf("Date = %q, want UTC RFC2822", sh.Date)
	}
	if sh.ContentMD5 != md5Hex(body) {
		t.Errorf("ContentMD5 = %s, want %s", sh.ContentMD5, md5Hex(body))
	}
	wantString := strings.Join([]string{
		"POST", md5Hex(body), "application/json", sh.Date, path,
	}, "\n")
	if want := hmacSHA1Hex([]byte(wantString), "secret"); sh.Signature != want {
		t.Errorf("Signature = %s, want %s", sh.Signature, want)
	}
}

func TestSignRequest_GETEmptyBody(t *testing.T) {
	sh := signRequest(time.Unix(0, 0).UTC(), "GET", "/v2/origin/custom/x", nil, "s")
	if sh.ContentMD5 != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("GET Content-MD5 = %s, want MD5 пустой строки", sh.ContentMD5)
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"hook":"value","n":42}`)
	secret := "5a44c5dff55f3c15a4cce8d7c4cc27e207c7e189"
	sig := hmacSHA1Hex(body, secret)

	if !VerifyWebhookSignature(body, sig, secret) {
		t.Error("valid signature rejected")
	}
	if !VerifyWebhookSignature(body, strings.ToUpper(sig), secret) {
		t.Error("uppercase signature should be accepted (case-insensitive compare)")
	}
	if VerifyWebhookSignature(body, sig, "wrong-secret") {
		t.Error("wrong secret accepted")
	}
	if VerifyWebhookSignature([]byte("tampered"), sig, secret) {
		t.Error("tampered body accepted")
	}
	if VerifyWebhookSignature(body, "sha1="+sig, secret) {
		t.Error("signature with sha1= prefix must NOT match (amoJo шлёт голый hex)")
	}
}
