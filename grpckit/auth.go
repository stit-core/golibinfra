package grpckit

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Authenticator — порт аутентификации (реализует потребитель, напр. access-домен
// гейтвея). Валидирует bearer-токен из metadata и возвращает ctx с identity.
// Ошибка → codes.Unauthenticated. grpckit владеет механикой интерсептора;
// проверка токена — на стороне потребителя.
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

func bearerToken(ctx context.Context) string {
	v := metadataFirst(ctx, authMetadataKey)
	if v == "" {
		return ""
	}
	if len(v) > 7 && strings.EqualFold(v[:7], "Bearer ") {
		return v[7:]
	}
	return v
}

func authenticate(ctx context.Context, a Authenticator) (context.Context, error) {
	tok := bearerToken(ctx)
	if tok == "" {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	newCtx, err := a.Authenticate(ctx, tok)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	return newCtx, nil
}

func authUnary(a Authenticator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isAuthExempt(info.FullMethod) {
			return handler(ctx, req)
		}
		newCtx, err := authenticate(ctx, a)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

func authStream(a Authenticator) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isAuthExempt(info.FullMethod) {
			return handler(srv, ss)
		}
		newCtx, err := authenticate(ss.Context(), a)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: newCtx})
	}
}
