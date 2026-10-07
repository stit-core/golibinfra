// Package redact — URL без секретов для текстов ошибок, логов и спанов китов: пароль и токен из
// NATS_URL, REDIS_URL, endpoint'ов S3 и OTLP, query исходящего запроса.
package redact

import (
	"errors"
	"net/url"
	"strings"
)

// Marker — заглушка на месте секрета; её же pgx ставит на место пароля DSN.
const Marker = "xxxxx"

// URL — URL подключения из конфига без секретов: всё от схемы до последнего `@` (userinfo; в
// позиции пользователя живёт и токен) — Marker, значения query — Marker, фрагмента нет, путь
// остаётся. Разбор не нужен: пароль с неэкранированными `/`, `?`, `#` разбор читает как порт,
// путь, query и фрагмент, а в URL подключения `@` вне userinfo не бывает. Так же — без схемы
// (`user:pass@host:port`).
func URL(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			return Marker + raw[at:]
		}
		return raw
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = Marker + rest[at:]
	}
	rest, _, _ = strings.Cut(rest, "#")
	if head, query, hasQuery := strings.Cut(rest, "?"); hasQuery {
		rest = head + "?" + maskQuery(query)
	}
	return scheme + "://" + rest
}

// RequestURL — URL исходящего запроса без секретов: хост и путь остаются (`server.address` и
// маршрут спана), userinfo, который узнал разбор, и значения query — Marker, фрагмента нет. `@` в
// пути запроса законен (`/calendars/a@b.com`), но так же разбор кладёт в путь хвост пароля с `/`:
// путь до последнего `@` — Marker. Неразбираемый URL и `@` во фрагменте — как у URL.
func RequestURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || strings.Contains(u.Fragment, "@") {
		return URL(raw)
	}
	c := *u
	if c.User != nil {
		c.User = url.User(Marker)
	}
	if at := strings.LastIndex(c.Path, "@"); at >= 0 {
		c.Path, c.RawPath = "/"+Marker+c.Path[at:], ""
	}
	c.RawQuery = maskQuery(c.RawQuery)
	c.Fragment, c.RawFragment = "", ""
	return c.String()
}

// maskQuery заменяет значения параметров query, имена остаются; параметр без `=` сам может быть
// токеном и заменяется целиком.
func maskQuery(raw string) string {
	if raw == "" {
		return ""
	}
	params := strings.Split(raw, "&")
	for i, p := range params {
		if name, _, ok := strings.Cut(p, "="); ok {
			params[i] = name + "=" + Marker
		} else if p != "" {
			params[i] = Marker
		}
	}
	return strings.Join(params, "&")
}

// ParseError — ошибка разбора URL без цитаты ввода: *url.Error несёт сам URL, неверный escape и
// порт цитируют его куски, разбор go-redis — путь и номер базы, в том числе хвост пароля.
func ParseError(err error) error {
	var escape url.EscapeError
	if errors.As(err, &escape) {
		return errors.New("invalid percent-escape in URL")
	}
	return errors.New("malformed URL")
}
