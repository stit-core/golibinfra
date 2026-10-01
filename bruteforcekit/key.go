package bruteforcekit

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"git.stit.tech/stit-core/golibinfra/internal/clientip"
)

// ErrInvalidKey — пустой или слишком длинный subject, пустой либо неразбираемый ip.
var ErrInvalidKey = errors.New("bruteforcekit: invalid key")

// MaxSubjectLen — предел длины subject: значение длиннее не помещается в
// btree-индекс Postgres вместе с ip, и запрос падает уже в базе.
const MaxSubjectLen = 255

// NormalizeKey приводит пару (subject, ip) к канонической форме ключа: разные
// записи одного логина (`User@Example.com ` и `user@example.com`) и одного
// адреса (`::1` и `0:0:0:0:0:0:0:1`, `::ffff:203.0.113.7` и `203.0.113.7`)
// обязаны попадать в один бакет счётчика. subject — без пробелов по краям и в
// нижнем регистре; IPv6 при Config.IPv6Prefix < FullIPv6Prefix сворачивается до
// подсети — перебор адресов одной /64 не даёт обойти блокировку. Нарушение —
// ErrInvalidKey.
func (c Config) NormalizeKey(subject, ip string) (string, string, error) {
	subject = strings.ToLower(strings.TrimSpace(subject))
	switch {
	case subject == "":
		return "", "", fmt.Errorf("%w: subject is empty", ErrInvalidKey)
	case len(subject) > MaxSubjectLen:
		return "", "", fmt.Errorf("%w: subject is longer than %d bytes", ErrInvalidKey, MaxSubjectLen)
	case ip == "":
		return "", "", fmt.Errorf("%w: ip is empty", ErrInvalidKey)
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", "", fmt.Errorf("%w: ip %q is not an IP address", ErrInvalidKey, ip)
	}
	// Та же свёртка, что у ключа лимитера httpkit: один клиент — один ключ на весь сервис.
	return subject, clientip.Key(addr, c.IPv6Prefix).String(), nil
}
