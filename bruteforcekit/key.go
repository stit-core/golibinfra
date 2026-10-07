package bruteforcekit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf8"

	"git.stit.tech/stit-core/golibinfra/internal/clientip"
)

// ErrInvalidKey — пустой subject, subject с NUL или не в UTF-8 (Postgres TEXT такого не хранит),
// пустой либо неразбираемый ip.
var ErrInvalidKey = errors.New("bruteforcekit: invalid key")

// MaxSubjectLen — предел длины subject в ключе: длиннее любого логина (email — до 254 байт).
// Subject сверх него учитывается по SHA-256 — строка счётчика не растёт с вводом атакующего.
const MaxSubjectLen = 255

// hashedSubjectPrefix — приставка ключа длинного subject: заглавных букв в нормализованном
// subject не бывает, и с настоящим логином ключ не совпадёт.
const hashedSubjectPrefix = "SHA256:"

// NormalizeKey приводит пару (subject, ip) к канонической форме ключа: разные
// записи одного логина (`User@Example.com ` и `user@example.com`) и одного
// адреса (`::1` и `0:0:0:0:0:0:0:1`, `::ffff:203.0.113.7` и `203.0.113.7`)
// обязаны попадать в один бакет счётчика. subject — без пробелов по краям и в
// нижнем регистре, длиннее MaxSubjectLen — SHA-256 от него; IPv6 при
// Config.IPv6Prefix < FullIPv6Prefix сворачивается до подсети — перебор адресов
// одной /64 не даёт обойти блокировку. Нарушение — ErrInvalidKey.
func (c Config) NormalizeKey(subject, ip string) (string, string, error) {
	// UTF-8 — до ToLower: тот заменил бы битые байты на U+FFFD и склеил разные subject'ы.
	subject = strings.TrimSpace(subject)
	switch {
	case subject == "":
		return "", "", fmt.Errorf("%w: subject is empty", ErrInvalidKey)
	case !utf8.ValidString(subject):
		return "", "", fmt.Errorf("%w: subject is not valid UTF-8", ErrInvalidKey)
	case strings.IndexByte(subject, 0) >= 0:
		return "", "", fmt.Errorf("%w: subject contains NUL", ErrInvalidKey)
	case ip == "":
		return "", "", fmt.Errorf("%w: ip is empty", ErrInvalidKey)
	}
	subject = strings.ToLower(subject)
	if len(subject) > MaxSubjectLen {
		sum := sha256.Sum256([]byte(subject))
		subject = hashedSubjectPrefix + hex.EncodeToString(sum[:])
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", "", fmt.Errorf("%w: ip %q is not an IP address", ErrInvalidKey, ip)
	}
	// Та же свёртка, что у ключа лимитера httpkit: один клиент — один ключ на весь сервис.
	return subject, clientip.Key(addr, c.IPv6Prefix).String(), nil
}
