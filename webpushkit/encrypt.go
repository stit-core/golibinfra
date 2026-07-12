package webpushkit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

const (
	authSecretLen       = 16 // RFC 8291: auth secret подписки
	saltLen             = 16 // RFC 8188: record salt
	cekLen              = 16 // AES-128
	nonceLen            = 12 // AES-GCM nonce
	gcmTagLen           = 16
	uncompressedP256Len = 65 // 0x04 || X(32) || Y(32)

	// declaredRecordSize — поле rs заголовка aes128gcm. Фиксируем 4096, как в
	// примере RFC 8291 §5; фактическая запись меньше (rs — только максимум).
	declaredRecordSize = 4096

	aes128gcmHeaderLen = saltLen + 4 + 1 + uncompressedP256Len // salt||rs||idlen||keyid

	// maxPayloadBytes — потолок plaintext, при котором зашифрованное тело
	// укладывается в гарантированные push-сервисами 4096 байт:
	// 4096 - header(86) - GCM tag(16) - padding delimiter(1) = 3993.
	maxPayloadBytes = 4096 - aes128gcmHeaderLen - gcmTagLen - 1
)

// encryptPayload реализует RFC 8291 (aes128gcm content encoding поверх RFC 8188)
// для одной записи. asPriv (эфемерная пара сервера приложения) и salt
// инъектируются вызывающим: в проде — случайные (Sender.Send), в юнит-тесте —
// фиксированные из тест-вектора RFC 8291 §5. Это внутренний хук детерминизма,
// не расширяющий публичный API.
func encryptPayload(payload, uaPublicRaw, authSecret []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	uaPub, err := ecdh.P256().NewPublicKey(uaPublicRaw)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: p256dh not a valid P-256 point: %w", ErrSubscription)
	}
	ecdhSecret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: ecdh: %w", err)
	}
	asPublicRaw := asPriv.PublicKey().Bytes()

	// RFC 8291 §3.4: смешиваем ECDH-секрет с auth secret подписки. IKM =
	// HKDF(auth_secret, ecdh_secret, "WebPush: info"||0x00||ua_public||as_public, 32).
	keyInfo := make([]byte, 0, len("WebPush: info\x00")+2*uncompressedP256Len)
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPublicRaw...)
	keyInfo = append(keyInfo, asPublicRaw...)

	prkKey, err := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: hkdf extract (key): %w", err)
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: hkdf expand (ikm): %w", err)
	}

	// RFC 8188 §2.2: CEK/NONCE из record salt и IKM.
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: hkdf extract (prk): %w", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", cekLen)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: hkdf expand (cek): %w", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", nonceLen)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: hkdf expand (nonce): %w", err)
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: gcm: %w", err)
	}

	// Единственная (последняя) запись: RFC 8188 padding delimiter 0x02.
	plain := make([]byte, len(payload)+1)
	copy(plain, payload)
	plain[len(payload)] = 0x02
	ciphertext := gcm.Seal(nil, nonce, plain, nil)

	// Тело: salt || rs(uint32 be) || idlen(1) || keyid(as_public) || ciphertext.
	// idlen — длина keyid; для uncompressed P-256 всегда uncompressedP256Len.
	body := make([]byte, 0, aes128gcmHeaderLen+len(ciphertext))
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, declaredRecordSize)
	body = append(body, uncompressedP256Len)
	body = append(body, asPublicRaw...)
	body = append(body, ciphertext...)
	return body, nil
}
