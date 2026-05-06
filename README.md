# golibinfra

Инфраструктурные библиотеки для Go-сервисов компании. Импортируется как Go-модуль из бизнес-репозиториев.

```go
import "git.stit.tech/akadmin/golibinfra/pgkit"
import "git.stit.tech/akadmin/golibinfra/httpkit"
import "git.stit.tech/akadmin/golibinfra/telemetry"
import "git.stit.tech/akadmin/golibinfra/config"
import "git.stit.tech/akadmin/golibinfra/testkit"
```

## Доступ к репозиторию

Репо приватный, лежит на собственной Gitea (`git.stit.tech`). Чтобы `go get` / `go mod tidy` мог его подтянуть, нужно настроить три вещи: GOPRIVATE, git-аутентификацию и (опционально) SSH-overlay поверх HTTPS.

### Шаг 1. SSH-ключ в Gitea

```bash
# если ключа ещё нет
ssh-keygen -t ed25519 -C "you@stit.tech"

# залить публичный ключ в Gitea: https://git.stit.tech/-/user/settings/keys
cat ~/.ssh/id_ed25519.pub | xclip -selection clipboard

# проверить, что подключение работает
ssh -T git@git.stit.tech
```

### Шаг 2. GOPRIVATE

Сообщаем Go, что `git.stit.tech` — приватный (не идти через `proxy.golang.org`, не проверять `sum.golang.org`):

```bash
go env -w GOPRIVATE='git.stit.tech/*'
```

Альтернативно — переменная окружения в `~/.zshrc` / `~/.bashrc`:
```bash
export GOPRIVATE='git.stit.tech/*'
```

### Шаг 3. SSH вместо HTTPS

Go по умолчанию ходит за модулями через HTTPS. Чтобы использовать SSH-ключ из шага 1 — добавляем git-overlay в `~/.gitconfig`:

```bash
git config --global url."git@git.stit.tech:".insteadOf "https://git.stit.tech/"
```

После этого `go mod tidy` будет ходить за `git.stit.tech/akadmin/golibinfra` через SSH.

### Альтернатива: HTTPS + личный токен

Если SSH недоступен (CI без ssh-agent и т.п.) — можно через токен:

```bash
# Gitea → Settings → Applications → Generate Token (scope: read:repository)
# Сохранить в ~/.netrc:
cat >> ~/.netrc <<EOF
machine git.stit.tech
login your-username
password ${GITEA_TOKEN}
EOF
chmod 600 ~/.netrc
```

В CI пробрасывай `GITEA_TOKEN` секретом и пиши `.netrc` в job-prepare шаге.

### Проверка

```bash
mkdir /tmp/check && cd /tmp/check && go mod init check
go get git.stit.tech/akadmin/golibinfra@v0.3.0
# должно скачать без ошибок
```

## Использование в проекте

В `go.mod`:
```
require git.stit.tech/akadmin/golibinfra v0.3.0
```

`go mod tidy` подтянет.

## Принципы

- **Никакой продуктовой специфики.** Если файл знает про конкретные роли, домены, имена сервисов — он не здесь.
- **Без скрытых зависимостей.** Каждый пакет можно подключать независимо.
- **fx-first.** Каждый пакет экспортирует `Module` для проводки.
- **Observability-by-default.** Все компоненты инструментированы OTel.

## Пакеты

| Пакет | Ответственность |
|---|---|
| [`pgkit`](pgkit/) | `*pgkit.DB` фасад, Transactor, migrate-runner (auto on start), маппинг pg-ошибок |
| [`httpkit`](httpkit/) | Huma bootstrap, generic middlewares (recovery, request-id, auth-callback, otel) |
| [`telemetry`](telemetry/) | OTel init (trace/meter/log), OTLP exporters, `Span(ctx, name)` |
| [`logkit`](logkit/) | `Logger` интерфейс + JSON-реализация поверх slog с trace_id correlation |
| [`authnkit`](authnkit/) | `Hasher` интерфейс + Argon2id-реализация |
| [`config`](config/) | Загрузчик: env + Vault, провайдит `[]Source` для приложения |
| [`testkit`](testkit/) | testcontainers (postgres), httptest helpers |

## Стабильность API

Публичные структуры конфигов (`pgkit.PoolConfig`, `httpkit.Config`,
`telemetry.Config`, `logkit.Config`, `authnkit.Argon2idParams`) — **part of public API**.
Менять имена/типы полей или их envconfig-теги = breaking change, идёт мажорным
апгрейдом. Продукты собирают свой `App`-конфиг из этих структур и зависят от
их стабильности.

## Версионирование

Семвер. Теги вида `v0.x.y`. До `v1.0.0` — API считается нестабильным, breaking changes допустимы в minor.

```bash
# подъём минорной версии
git tag v0.3.0
git push origin v0.3.0
```

В шаблонах/проектах после релиза:
```bash
go get git.stit.tech/akadmin/golibinfra@v0.3.0
go mod tidy
```

## Локальная разработка с правкой golibinfra

Когда правишь `golibinfra` параллельно с продуктом — добавь временный `replace` в `go.mod` продукта:
```
replace git.stit.tech/akadmin/golibinfra => ../golibinfra
```

**Не коммить эту строку** — это для local-only ветки. Перед мержем убрать и поднять версию через `go get`.

## Лицензия

Internal. Stit Tech.
