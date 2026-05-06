# httpkit

HTTP-bootstrap: chi + Huma + middleware'ы.

## Что даёт

- `NewServer(cfg)` — chi-роутер + Huma API + http.Server с таймаутами
- Middleware'ы: `RequestID`, `Recovery`, `AccessLog`, `Auth(extract)`, `OTel`
- `Module` — fx-модуль: `*Server`, `huma.API`, lifecycle-хуки старта/остановки
- `AuthExtractor` — callback-точка для продукт-специфичной аутентификации (не знает про роли/JWT)

## Подключение auth

В продуктовом репо:

```go
// internal/platform/authn/jwt.go
func ExtractJWT(r *http.Request) (context.Context, error) {
    claims, err := parseJWT(r.Header.Get("Authorization"))
    if err != nil { return nil, err }
    id := identity.Identity{UserID: claims.Sub, Role: identity.Role(claims.Role)}
    return identity.With(r.Context(), id), nil
}

// cmd/api/main.go
fx.New(
    httpkit.Module,
    fx.Provide(func() httpkit.AuthExtractor { return authn.ExtractJWT }),
)
```

Если AuthExtractor не предоставлен — middleware no-op, доступ публичный.

## Регистрация роутов

Каждый домен в своём `module.go` через `fx.Invoke`:

```go
fx.Invoke(func(api huma.API, h *Handler) { h.Register(api) })
```
