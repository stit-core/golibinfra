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

// URL — raw без секретов: userinfo целиком (в позиции пользователя живёт и токен), значения
// query и фрагмент заменены, путь остаётся. Так же — у URL без схемы (`user:pass@host:port`) и у
// неразбираемого URL: userinfo там — всё до последнего `@` перед query, пароль бывает с `/`.
func URL(raw string) string {
	if !strings.Contains(raw, "://") {
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			return Marker + raw[at:]
		}
		return raw
	}
	if u, err := url.Parse(raw); err == nil {
		c := *u
		if c.User != nil {
			c.User = url.User(Marker)
		}
		c.RawQuery = maskQuery(c.RawQuery)
		c.Fragment, c.RawFragment = "", ""
		return c.String()
	}
	scheme, rest, _ := strings.Cut(raw, "://")
	rest, _, _ = strings.Cut(rest, "#")
	head, query, hasQuery := strings.Cut(rest, "?")
	if at := strings.LastIndex(head, "@"); at >= 0 {
		head = Marker + head[at:]
	}
	if hasQuery {
		head += "?" + maskQuery(query)
	}
	return scheme + "://" + head
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

// ParseError — ошибка разбора URL без цитаты ввода: *url.Error несёт сам URL, а неверный escape
// и порт цитируют его куски, в том числе пароль; прочие ошибки — как есть.
func ParseError(err error) error {
	var escape url.EscapeError
	if errors.As(err, &escape) {
		return errors.New("invalid percent-escape in URL")
	}
	var parse *url.Error
	if errors.As(err, &parse) {
		return errors.New("malformed URL")
	}
	return err
}
