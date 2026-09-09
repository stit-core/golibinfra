package amocrmkit

import (
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // amoJo Content-MD5 требует MD5 — диктует протокол, не выбор
	"crypto/sha1" //nolint:gosec // amoJo X-Signature требует HMAC-SHA1 — диктует протокол, не выбор
	"encoding/hex"
	"strings"
	"time"
)

// Исходящий запрос и входящий вебхук amoJo подписываются по-разному: 5-компонентная
// строка против HMAC по сырому телу (docs/architecture.md § amoJo).

// rfc2822 — формат заголовка Date amoJo. Подпись валидна 15 минут; зона может
// быть любой, но безопаснее UTC (меньше зависимость от системной зоны).
const rfc2822 = "Mon, 02 Jan 2006 15:04:05 -0700"

// signedHeaders — заголовки, которыми снабжается каждый исходящий amoJo-запрос.
type signedHeaders struct {
	Date       string
	ContentMD5 string
	Signature  string
}

// signRequest считает Content-MD5 + X-Signature для исходящего amoJo-запроса.
// path — путь без протокола/домена/query (для amoJo: /v2/origin/custom{...}).
// body — точные байты тела (для GET — nil/пусто, MD5 пустой строки).
func signRequest(now time.Time, method, path string, body []byte, secret string) signedHeaders {
	date := now.UTC().Format(rfc2822)
	contentMD5 := md5Hex(body)
	signingString := strings.Join([]string{
		strings.ToUpper(method),
		contentMD5,
		"application/json",
		date,
		path,
	}, "\n")
	return signedHeaders{
		Date:       date,
		ContentMD5: contentMD5,
		Signature:  hmacSHA1Hex([]byte(signingString), secret),
	}
}

// VerifyWebhookSignature проверяет подпись входящего amoJo-вебхука: HMAC-SHA1
// по НЕТРОНУТОМУ телу (без trim/re-marshal), сравнение в постоянное время.
// receivedSignature — значение заголовка X-Signature (голый hex, БЕЗ префикса sha1=).
func VerifyWebhookSignature(rawBody []byte, receivedSignature, secret string) bool {
	expected := hmacSHA1Hex(rawBody, secret)
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(receivedSignature)))
}

func md5Hex(b []byte) string {
	h := md5.Sum(b) //nolint:gosec // Content-MD5 — протокол amoJo
	return hex.EncodeToString(h[:])
}

func hmacSHA1Hex(msg []byte, secret string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(msg)
	return hex.EncodeToString(mac.Sum(nil))
}
