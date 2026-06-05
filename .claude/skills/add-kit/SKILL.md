---
name: add-kit
description: Добавить новый kit (пакет) в golibinfra по конвенциям библиотеки — package-doc, fx Module, опц. envconfig-конфиг, observability-by-default, тесты, testkit-сабпакет, строка в README и карте CLAUDE.md. Активируй когда юзер говорит "/add-kit", "добавь kit", "новый пакет в golibinfra", "заведи <name>kit", "нужен переиспользуемый примитив X". Для правки существующего kit'а скилл НЕ нужен — это прямой Edit.
---

# Skill: add-kit

Заводит новый переиспользуемый примитив как отдельный пакет-kit. golibinfra —
общая lib: сюда едет только то, что не специфично для бизнес-домена (доменное
живёт в сервис-репо).

## Когда применять

- "Нужен переиспользуемый kit для X" / "добавь `<name>kit`" / "/add-kit".
- Примитив (generic util, middleware, клиент, helper) нужен ≥2 сервисам или
  концептуально инфраструктурный.

## Когда НЕ применять

- Правка существующего kit'а → прямой Edit.
- Логика, специфичная для одного бизнес-домена → она в сервис-репо, не сюда.

## Шаги

1. **Слаг.** Имя пакета — `<area>kit` (нижний регистр, без подчёркиваний): `cachekit`,
   `authnkit`. Спроси у пользователя назначение и публичный API, если неочевидно.
2. **Каталог пакета** `<kit>/`:
   - `<kit>.go` — реализация + package-doc (одна-две строки: что это и зачем).
   - `module.go` — `var Module = fx.Module("<kit>", fx.Provide(...))` с конструкторами.
   - `<kit>_test.go` — тесты (unit; testcontainers — через `testkit`, если нужен I/O).
3. **Конфиг (если есть env-параметры).** Структура с `envconfig`-тегами — это
   **public API**: имена полей/тегов попадут в `README.md § Стабильность API`.
   Грузится через `config`-композицию (ENV + Vault), не хардкодь значения.
4. **Observability by default.** Если kit делает I/O или несёт нагрузку — эмить
   метрики/спаны через otel-go SDK, читая resource attrs из `telemetry.Config`
   (через fx). Не выставляй `tenant`/`service.namespace` — их ставит control-plane.
5. **testkit-сабпакет (опц.).** Нужны helpers для тестов консьюмера — заведи
   `testkit/<name>test/`, где `<name>` — имя кита **без** суффикса `kit`
   (`cachekit` → `testkit/cachetest/`; см. соседние `authntest` от `authnkit`,
   `bruteforcetest` от `bruteforcekit`).
6. **Документация.** Добавь строку в `README.md § Пакеты` (slug + ответственность +
   что эмитит) и в карту пакетов в `CLAUDE.md`. Без этого kit «невидим».
7. **Gate.** `golangci-lint run ./...` + `go test -short -race ./...` + `go build ./...`
   зелёные перед claim «готово».

## Следующий шаг

После зелёного gate — релиз новой версии через `/release` (consumer'ы подтянут kit
через `go get @vX.Y.Z`). Новый сторонний import в `go.mod` — сперва согласуй с пользователем.
