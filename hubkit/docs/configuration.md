# hubkit — конфигурация

Каждый сабпакет читает свои переменные через `New*Config()` и валидирует их
(`Validate()` → sentinel `ErrConfig` пакета). Невалидный конфиг роняет старт
приложения: «хаб недоступен» в рантайме и 401 на первом запросе — худшие
диагностики для потерянной переменной деплоя.

`Module` кита ENV не читает — конструктор конфига добавляет caller
(`fx.Provide(sso.NewConfig)` и т.д.).

## sso (продукт верифицирует сессию)

| ENV | Тип | Default | Правило `Validate` |
|---|---|---|---|
| `HUB_SSO_JWKS_URL` | string | — | обязателен, абсолютный http(s) URL |
| `HUB_SSO_AUDIENCE` | string | — | обязателен; равен `HUB_SESSION_AUDIENCE` issuer'а сотрудника |
| `HUB_SSO_ISSUER` | string | `https://id.stit.tech` | обязателен (явно пустое значение — отказ старта) |
| `HUB_SSO_COOKIE` | string | `stit_session` | непустое; отличается от `HUB_SSO_CUSTOMER_COOKIE` |
| `HUB_SSO_LOGIN_URL` | string | `https://id.stit.tech/auth/login` | абсолютный http(s) URL |
| `HUB_SSO_LEEWAY` | duration | `60s` | `>= 0`; допуск часов для `exp`/`nbf`/`iat` |
| `HUB_SSO_KEY_GRACE` | duration | `15m` | `>= 0`; берётся не меньше `HUB_SESSION_TTL` issuer'а |
| `HUB_SSO_SECURITY_SCHEME` | string | `EmployeeSession` | непустое; отличается от схемы заказчика |
| `HUB_SSO_CUSTOMER_JWKS_URL` | string | — | пусто → контур заказчика выключен; иначе абсолютный http(s) URL |
| `HUB_SSO_CUSTOMER_AUDIENCE` | string | — | обязателен при включённом контуре |
| `HUB_SSO_CUSTOMER_ISSUER` | string | `https://api.hub.stit.tech` | обязателен при включённом контуре |
| `HUB_SSO_CUSTOMER_COOKIE` | string | `stit_customer` | непустое, отличается от cookie сотрудника |
| `HUB_SSO_CUSTOMER_LOGIN_URL` | string | `https://hub.stit.tech/customer/login` | абсолютный http(s) URL при включённом контуре |
| `HUB_SSO_CUSTOMER_SECURITY_SCHEME` | string | `CustomerSession` | непустое, отличается от схемы сотрудника |

`HUB_SSO_KEY_GRACE` — сколько выведенный из JWKS `kid` продолжает проверять
подписи после того, как verifier перечитал набор. Значение меньше TTL
access-токена даёт 401 живым сессиям в момент ротации ключа issuer'а. Оно же —
вторая половина окна отзыва ключа: продукт перестаёт доверять снятому ключу
через `HUB_SESSION_JWKS_MAX_AGE` issuer'а (перечитывание набора) плюс это
значение (см. [architecture.md § Ротация ключа подписи](architecture.md)).

`HUB_SSO_LEEWAY` покрывает расхождение часов между issuer'ом и продуктом. Токен
без `exp` отвергается независимо от leeway.

Имена security-схем — часть контракта фронта с OpenAPI продукта; каноническое
имя в экосистеме не зафиксировано, поэтому оба настраиваются, а дефолты —
`EmployeeSession` / `CustomerSession`.

## issuer (employee-backend, company-backend)

### Ключи (`KeyConfig`)

| ENV | Тип | Default | Правило `Validate` |
|---|---|---|---|
| `HUB_SESSION_PRIVATE_KEY` | PEM | — | активный ключ: приватный P-256 (SEC1 или PKCS#8) |
| `HUB_SESSION_PRIVATE_KEY_FILE` | path | — | путь к тому же PEM; приоритетнее inline |
| `HUB_SESSION_NEXT_PRIVATE_KEY` | PEM | — | следующий ключ ротации: приватный или публичный PKIX |
| `HUB_SESSION_NEXT_PRIVATE_KEY_FILE` | path | — | путь к next-ключу |
| `HUB_SESSION_PREVIOUS_PUBLIC_KEY` | PEM | — | предыдущий ключ: публичный PKIX (или приватный — берётся публичная часть) |
| `HUB_SESSION_PREVIOUS_PUBLIC_KEY_FILE` | path | — | путь к previous-ключу |
| `HUB_SESSION_ISSUER` | string | `https://id.stit.tech` | обязателен, абсолютный http(s) URL |
| `HUB_SESSION_AUDIENCE` | string | — | обязателен (`sso` проверяет `aud` всегда) |
| `HUB_SESSION_JWKS_MAX_AGE` | duration | `5m` | `>= 0`; `Cache-Control: max-age` ответа JWKS |

`HUB_SESSION_JWKS_MAX_AGE` задаёт не только кэш посредников: `hubkit/sso` берёт
из него период перечитывания набора (не меньше минуты и не больше 15 минут —
потолок `internal/jwks`). Это окно, за которое продукты узнают о новом `kid` и
забывают снятый.

Хотя бы один из `HUB_SESSION_PRIVATE_KEY` / `HUB_SESSION_PRIVATE_KEY_FILE`
обязателен; ключ обязан быть ECDSA P-256, иначе `ErrNotEC`.

### Cookie (`CookieConfig`)

| ENV | Тип | Default | Правило `Validate` |
|---|---|---|---|
| `HUB_SESSION_COOKIE` | string | `stit_session` | непустое, отличается от refresh-cookie |
| `HUB_SESSION_COOKIE_DOMAIN` | string | `.stit.tech` | — |
| `HUB_SESSION_COOKIE_SECURE` | bool | `true` | `false` только в dev без TLS |
| `HUB_SESSION_TTL` | duration | `15m` | `> 0`; срок access-сессии |
| `HUB_SESSION_REFRESH_COOKIE` | string | `stit_refresh` | непустое |
| `HUB_SESSION_REFRESH_COOKIE_PATH` | string | `/auth` | начинается с `/` |

### Refresh (`RefreshConfig`)

| ENV | Тип | Default | Правило `Validate` |
|---|---|---|---|
| `HUB_REFRESH_TTL` | duration | `720h` | `> 0` |
| `HUB_REFRESH_TOMBSTONE_TTL` | duration | `720h` | `>= HUB_REFRESH_TTL` |
| `HUB_REFRESH_REUSE_GRACE` | duration | `10s` | `>= 1s` и `< HUB_REFRESH_TTL` |

`HUB_REFRESH_REUSE_GRACE` — окно, внутри которого повторное предъявление того же
refresh-токена считается параллельным обменом (две вкладки, ретрай после
обрыва), а не утечкой: см. [architecture.md § Rotating refresh](architecture.md).
Минимум — 1s, и это fail-fast: с нулевым окном гонка двух вкладок снова
отзывала бы семейство, поэтому такое значение роняет старт (`ErrConfig`), а не
проявляется на первом параллельном refresh'е.

**Сайзинг Redis.** Каждый обмен оставляет маркер `hub:refresh:used:<sha256>`
(ключ 81 байт + значение), живущий `HUB_REFRESH_TTL` — это цена
reuse-detection: без маркера повтор утёкшего токена неотличим от неизвестного.
Число маркеров одной активной сессии = `HUB_REFRESH_TTL` / TTL access-токена:
при дефолтах (`720h` и `HUB_SESSION_TTL=15m`) — до ~2 900 ключей, порядка
полумегабайта на сессию, то есть ~0,5 ГиБ на тысячу непрерывно активных
сессий. Уменьшать это стоит через `HUB_REFRESH_TTL` (короче окно бездействия до
повторного логина), а не через access-TTL: более длинный access-токен
откладывает реакцию на отзыв сессии.

## client (m2m-доступ к hub API)

| ENV | Тип | Default | Правило `Validate` |
|---|---|---|---|
| `HUB_BASE_URL` | string | — | обязателен, абсолютный http(s) URL company-backend |
| `HUB_EMPLOYEE_BASE_URL` | string | — | абсолютный http(s) URL, если задан; пусто → `HUB_BASE_URL` |
| `HUB_TAG_BASE_URL` | string | — | абсолютный http(s) URL, если задан; пусто → `HUB_BASE_URL` |
| `HUB_SERVICE_TOKEN` | string | — | обязателен: m2m Bearer, значение из реестра сервис-токенов хаба |
| `HUB_SERVICE_NAME` | string | — | имя сервиса в заголовке `X-Hub-Service` |

## sync (движок проекций)

| ENV | Тип | Default | Правило `Validate` |
|---|---|---|---|
| `HUB_SYNC_INTERVAL` | duration | `30s` | `> 0` |
| `HUB_SYNC_LIMIT` | int | `500` | `> 0` и `<= 10000` |
| `HUB_SYNC_TIMEOUT` | duration | `5m` | `>= 0`; `0` — тик без дедлайна |
| `HUB_SYNC_JITTER` | duration | `5s` | `>= 0`; разводит тики нескольких ресурсов |
| `HUB_SYNC_RUN_ON_START` | bool | `true` | первый тик сразу после старта |

`HUB_SYNC_LIMIT` — размер страницы и признак «догнали»: страница короче лимита
завершает тик.
