package cachekit

import (
	"bytes"
	"context"
	"crypto/rand"
	"time"
)

const idempotencyPrefix = "idempotency:"

// reservedPrefix занимает ключ на время выполнения запроса; Get такой ключ не
// отдаёт, чтобы httpkit различал «выполняется» и «готовый ответ». За префиксом
// идёт токен владельца резерва — запись всегда JSON, коллизии нет.
const reservedPrefix = "\x00reserved:"

func reservedValue(token string) []byte { return []byte(reservedPrefix + token) }

// IdempotencyStore — байтовый store для httpkit.Idempotency поверх Cache:
// ключи в namespace `idempotency:`, резерв через SetNX.
type IdempotencyStore struct {
	c Cache
}

// NewIdempotencyStore оборачивает Cache в store идемпотентности.
func NewIdempotencyStore(c Cache) *IdempotencyStore {
	return &IdempotencyStore{c: c}
}

// Reserve занимает ключ маркером резерва на ttl и возвращает токен владельца;
// ok=false — резерв уже держит другой запрос или по ключу лежит готовая запись.
func (s *IdempotencyStore) Reserve(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	token := rand.Text()
	ok, err := s.c.SetNX(ctx, idempotencyPrefix+key, reservedValue(token), ttl)
	if err != nil || !ok {
		return "", false, err
	}
	return token, true, nil
}

// Get отдаёт готовую запись; ключ под резервом — found == false.
func (s *IdempotencyStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, found, err := s.c.Get(ctx, idempotencyPrefix+key)
	if err != nil || !found || bytes.HasPrefix(v, []byte(reservedPrefix)) {
		return nil, false, err
	}
	return v, true, nil
}

// Set кладёт готовую запись на ttl поверх резерва владельца token; ok=false — ключ уже не держит
// этот резерв (истёк ReserveTTL, его занял другой запрос): запись не пишется.
func (s *IdempotencyStore) Set(ctx context.Context, key, token string, record []byte, ttl time.Duration) (bool, error) {
	return s.c.CompareAndSet(ctx, idempotencyPrefix+key, reservedValue(token), record, ttl)
}

// Release снимает резерв. Ключ удаляется, только если он всё ещё держит маркер
// с токеном владельца: ни готовая запись, ни резерв следующего запроса
// (истёк ReserveTTL) опоздавшим Release не сносятся.
func (s *IdempotencyStore) Release(ctx context.Context, key, token string) error {
	_, err := s.c.CompareAndDelete(ctx, idempotencyPrefix+key, reservedValue(token))
	return err
}
