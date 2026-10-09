package httpkit

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"git.stit.tech/stit-core/golibinfra/internal/clientip"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ClientIPFrom — IP клиента, который ClientIP кладёт в ctx (в цепочке Module — до OTel), в
// канонической форме (IPv4-mapped — IPv4); пустая строка — ClientIP в цепочке не было. Тот же
// адрес — ключ лимитера, client.address server-спана, адрес для аудита и bruteforcekit.
func ClientIPFrom(ctx context.Context) string {
	if addr := clientip.From(ctx); addr.IsValid() {
		return addr.String()
	}
	return ""
}

// ClientIP кладёт IP клиента в ctx (ClientIPFrom). trustProxyHops (HTTP_TRUST_PROXY_HOPS) —
// сколько доверенных прокси перед сервисом дописывают X-Forwarded-For: 0 — адрес соединения,
// N — N-я справа запись X-Forwarded-For (IP или ip:port), левее неё — то, что прислал сам клиент;
// запись короче N или не IP — адрес соединения. X-Real-IP не читается. X-Forwarded-For при 0 — одна
// строка Warn на middleware: за прокси без настройки все клиенты делят его адрес. nil-логгер —
// logkit.Nop().
func ClientIP(trustProxyHops int, log logkit.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = logkit.Nop()
	}
	var warnOnce sync.Once
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			addr := remoteAddr(r)
			xff := r.Header.Values("X-Forwarded-For")
			switch {
			case trustProxyHops > 0:
				if fwd, ok := forwardedFor(xff, trustProxyHops); ok {
					addr = fwd
				}
			case len(xff) > 0:
				warnOnce.Do(func() {
					log.Warn(r.Context(), "x-forwarded-for ignored: HTTP_TRUST_PROXY_HOPS=0, client ip is the peer address",
						"peer", addr.String())
				})
			}
			next.ServeHTTP(w, r.WithContext(clientip.With(r.Context(), addr)))
		})
	}
}

// forwardedFor — hops-я справа запись X-Forwarded-For по всем заголовкам по порядку.
func forwardedFor(headers []string, hops int) (netip.Addr, bool) {
	entries := strings.Split(strings.Join(headers, ","), ",")
	if len(entries) < hops {
		return netip.Addr{}, false
	}
	entry := strings.TrimSpace(entries[len(entries)-hops])
	if addr, err := netip.ParseAddr(entry); err == nil {
		return addr, true
	}
	// Часть прокси дописывает порт: без разбора ip:port все клиенты делили бы адрес прокси.
	ap, err := netip.ParseAddrPort(entry)
	return ap.Addr(), err == nil
}

// remoteAddr — адрес соединения: у net/http это host:port, у тестового запроса бывает голый IP.
func remoteAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr()
	}
	addr, _ := netip.ParseAddr(r.RemoteAddr)
	return addr
}
