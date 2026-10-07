package cachekit

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// redisLog — мост внутреннего логгера go-redis в logkit: строка пула или переподключения — поле
// detail записи WARN "redis" в общем sink (JSON, маски, trace_id), а не текст в stderr; та же
// форма, что у моста grpc (internal/grpclogkit).
var redisLog = &redisLogBridge{}

// redis.SetLogger не потокобезопасен: ставится в init, до первого клиента; дальше меняется только
// цель моста, атомарно.
func init() { redis.SetLogger(redisLog) }

type redisLogBridge struct{ target atomic.Pointer[logkit.Logger] }

// install направляет лог go-redis в log. Логгер у go-redis один на процесс: из настоящих
// выигрывает последний, nil и Nop мост не занимают — иначе кэш без логгера глушил бы чужой.
func (b *redisLogBridge) install(log logkit.Logger) {
	if log == nil || log == logkit.Nop() {
		return
	}
	b.target.Store(&log)
}

// Printf — контракт логгера go-redis: строка без уровня, поэтому WARN. Пока логгера нет, строка
// идёт в stderr, как у go-redis без моста.
func (b *redisLogBridge) Printf(ctx context.Context, format string, v ...any) {
	text := fmt.Sprintf(format, v...)
	if p := b.target.Load(); p != nil {
		(*p).Warn(ctx, "redis", "detail", text)
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "redis:", text)
}
