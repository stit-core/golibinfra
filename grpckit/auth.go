package grpckit

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Authenticator — порт аутентификации (реализует потребитель, напр. access-домен
// гейтвея). Валидирует bearer-токен из metadata и возвращает ctx с identity.
// Ошибка → codes.Unauthenticated с обезличенным текстом; деталь ошибки порта
// уходит только в лог. grpckit владеет механикой интерсептора; проверка токена —
// на стороне потребителя.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (context.Context, error)
}

const authMetadataKey = "authorization"

// authExemptPrefixes — методы без auth (health, reflection).
var authExemptPrefixes = []string{
	"/grpc.health.v1.Health/",
	"/grpc.reflection.",
}

func isAuthExempt(fullMethod string) bool {
	for _, p := range authExemptPrefixes {
		if strings.HasPrefix(fullMethod, p) {
			return true
		}
	}
	return false
}

const bearerPrefix = "bearer "

// bearerToken принимает и «Bearer <token>» (регистр префикса любой), и голый
// токен; пустой остаток после префикса — не токен, а отсутствие токена, иначе
// порт аутентификации зря дёргает свой стор.
func bearerToken(ctx context.Context) string {
	v := metadataFirst(ctx, authMetadataKey)
	if len(v) >= len(bearerPrefix) && strings.EqualFold(v[:len(bearerPrefix)], bearerPrefix) {
		return strings.TrimSpace(v[len(bearerPrefix):])
	}
	return strings.TrimSpace(v)
}

// authenticate не отдаёт клиенту текст ошибки порта: он может нести внутренние
// детали (имя ключа, причина истечения, состояние стора).
func authenticate(ctx context.Context, a Authenticator, log logkit.Logger, method string) (context.Context, error) {
	tok := bearerToken(ctx)
	if tok == "" {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	newCtx, err := a.Authenticate(ctx, tok)
	if err != nil {
		log.Warn(ctx, "grpc auth rejected", "method", method, "error", err.Error())
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return newCtx, nil
}

func authUnary(a Authenticator, log logkit.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isAuthExempt(info.FullMethod) {
			return handler(ctx, req)
		}
		newCtx, err := authenticate(ctx, a, log, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

func authStream(a Authenticator, log logkit.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isAuthExempt(info.FullMethod) {
			return handler(srv, ss)
		}
		newCtx, err := authenticate(ss.Context(), a, log, info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: newCtx})
	}
}
