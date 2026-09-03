// Package amocrmkit — чистый транспортный клиент к amoCRM: REST API v4 (Bearer),
// amoJo Chat API (HMAC-SHA1), OAuth2, Drive, per-account rate-limit и таксономия
// ошибок. Stateless: ни persistence, ни очередей, ни бизнес-логики, ни
// мультиаккаунт-оркестрации — это слой сервиса-потребителя.
//
// Параметры аккаунта (subdomain/tld/token, channel-secret для amoJo,
// client_id/secret/code для OAuth) приходят аргументами вызовов, не глобальной
// конфигурацией: один Client обслуживает все аккаунты, выбор аккаунта — у
// потребителя. Subdomain и TLD валидируются до сборки URL (ErrInvalidAccount).
//
// Собирает свой httpclient (retry + circuit-breaker + OTel; каждая попытка
// проходит через лимитер аккаунта), использует clockkit и logkit. Метрики —
// amocrm_api_calls_total{endpoint,status}, amocrm_api_duration_seconds{endpoint},
// amocrm_rate_limited_total{account}, amocrm_oauth_refresh_total{status}.
package amocrmkit
