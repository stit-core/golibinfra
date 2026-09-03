---
name: add-kit
description: Добавить новый kit (пакет) в golibinfra по конвенциям библиотеки — package-doc, fx Module, envconfig-конфиг с Validate, observability через internal/otelkit, healthkit-проба при внешней зависимости, тесты, testkit-сабпакет, README кита (+ docs/ у сложного) и строка-индекс в корневом README и карте CLAUDE.md. Активируй когда юзер говорит "/add-kit", "добавь kit", "новый пакет в golibinfra", "заведи <name>kit", "нужен переиспользуемый примитив X". Для правки существующего kit'а скилл НЕ нужен — это прямой Edit.
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
   - `<kit>.go` — реализация + package-doc (одна-две строки: что это и зачем, какие
     метрики эмитит).
   - `module.go` — `var Module = fx.Module("<kit>", fx.Provide(...))` с конструкторами
     и lifecycle (`OnStart` — fail-fast подключение, `OnStop` — `Close`).
   - `<kit>_test.go` — тесты (unit; testcontainers — через `testkit`, если нужен I/O;
     образы с конкретным тегом, никогда `:latest`).
3. **Конфиг (если есть env-параметры).** Структура с `envconfig`-тегами — это
   **public API**. `NewConfig() (Config, error)` читает ENV и вызывает
   `Config.Validate()` (sentinel `ErrConfig`); `Module` конфиг **не** provide'ит —
   его даёт caller. Каждый тег описывается в README кита (`make docs-check`).
4. **Логирование и observability.** Логгер — `logkit.Logger` из fx-графа.
   Метрики/спаны — через `internal/otelkit` (`otelkit.Meter("<kit>")`,
   `otelkit.Must(...)`): инертны без провайдера у потребителя. Имена —
   `<kit_prefix>_<what>_<unit|total>`, label'ы — конечные множества.
   Кит с внешней зависимостью отдаёт `Probe(...) healthkit.Probe` для `/ready`.
5. **Ошибки и таймауты.** Sentinel-ошибки с префиксом `<kit>:`; каждый исходящий
   вызов — с таймаутом (никакого `http.DefaultClient`); всё, что может быть отменено,
   принимает `ctx`.
6. **testkit-сабпакет (опц.).** Нужны helpers для тестов консьюмера — заведи
   `testkit/<name>test/`, где `<name>` — имя кита **без** суффикса `kit`
   (`cachekit` → `testkit/cachetest/`; см. `authntest`, `bruteforcetest`).
7. **Документация.** Заведи `<kit>/README.md` (назначение, импорт, быстрый старт,
   конфигурация, observability, API, инварианты) — единый источник правды по
   киту. Для сложного кита разнеси детали по `<kit>/docs/` (`architecture.md`,
   `configuration.md`, `observability.md`, `usage.md`, `testing.md`). Плюс строка
   в `README.md § Пакеты` (и в env-таблицу, если кит читает endpoint) и в карту
   пакетов `CLAUDE.md`. Без README kit «невидим».
8. **Gate.** `make check` зелёный перед claim «готово».

## Следующий шаг

После зелёного gate — релиз новой версии через `/release` (consumer'ы подтянут kit
через `go get @vX.Y.Z`). Новый сторонний import в `go.mod` — сперва согласуй с пользователем.
