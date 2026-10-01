// Package authnkit — кирпичики аутентификации: Hasher (Argon2id), HS256-JWT
// поверх go-jose с типом токена, TOTP (RFC 6238), одноразовые токены и
// экземплярный RBAC-реестр.
// Auth-flow'ы (login, refresh, MFA enrollment) — за потребителем; сигналы кита
// перечислены в docs/observability.md.
package authnkit
