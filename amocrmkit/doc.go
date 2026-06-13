// Package amocrmkit — чистый транспортный клиент к amoCRM: REST API v4 (Bearer),
// amoJo Chat API (HMAC-SHA1), OAuth2, per-account rate-limit (7 rps) и таксономия
// ошибок. Stateless: ни persistence, ни NATS, ни outbox, ни бизнес-логики,
// ни мультиаккаунт-оркестрации — это слой gateway-сервиса.
//
// Параметры мультиаккаунта (subdomain/tld/token, channel-secret для amoJo,
// client_id/secret/code для OAuth) приходят аргументами вызовов, не глобальной
// конфигурацией: один Client обслуживает все аккаунты, выбор аккаунта — у gateway.
//
// Обёртка над httpclient (retry + circuit-breaker + OTel), clockkit (TTL/refresh,
// тестируемость) и logkit. Метрики — amocrm_api_calls_total{endpoint,status},
// amocrm_api_duration_seconds{endpoint}, amocrm_rate_limited_total{account},
// amocrm_oauth_refresh_total{status}.
package amocrmkit
