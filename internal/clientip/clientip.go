// Package clientip — ctx-ключ и каноническая форма IP клиента: httpkit кладёт адрес в ctx,
// лимитер httpkit и bruteforcekit сворачивают его в ключ одной функцией.
package clientip

import (
	"context"
	"net/netip"
)

type ctxKey struct{}

// With кладёт адрес клиента в ctx в канонической форме (Canonical).
func With(ctx context.Context, addr netip.Addr) context.Context {
	return context.WithValue(ctx, ctxKey{}, Canonical(addr))
}

// From — адрес клиента из ctx; без него — нулевой netip.Addr (IsValid() == false).
func From(ctx context.Context) netip.Addr {
	addr, _ := ctx.Value(ctxKey{}).(netip.Addr)
	return addr
}

// Canonical — одна запись одного адреса: IPv4-mapped IPv6 — IPv4, без зоны (`fe80::1%eth0`
// значим только на хосте отправителя).
func Canonical(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}

// Key — ключ клиента для учёта попыток и запросов: IPv6 сворачивается до подсети /ipv6Prefix
// (провайдер выдаёт клиенту /64 целиком, и перебор её адресов не должен давать новый ключ);
// ipv6Prefix — 1…128, 128 — без свёртки.
func Key(addr netip.Addr, ipv6Prefix int) netip.Addr {
	addr = Canonical(addr)
	if !addr.Is6() || ipv6Prefix >= addr.BitLen() {
		return addr
	}
	prefix, err := addr.Prefix(ipv6Prefix)
	if err != nil {
		return addr
	}
	return prefix.Addr()
}
