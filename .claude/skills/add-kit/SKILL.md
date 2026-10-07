---
name: add-kit
description: Завести в golibinfra новый kit по конвенциям библиотеки — package-doc, fx Module, envconfig-конфиг с Validate, observability через internal/otelkit, healthkit-проба при внешней зависимости, тесты, testkit-сабпакет, README кита (+ docs/ у сложного) и строки в корневом README и карте CLAUDE.md. Используй, когда нужен новый переиспользуемый пакет — инфраструктурный примитив, клиент или helper для нескольких сервисов. Правка существующего кита — прямой Edit, без скилла.
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
   - `<kit>.go` — реализация + package-doc (1–3 строки: что это и зачем, какие
     метрики эмитит). Doc-comment на каждом экспортируемом идентификаторе — одна
     строка, начинается с имени (`revive.exported` в гейте).
   - `module.go` — `var Module = fx.Module("<kit>", fx.Provide(...))` с конструкторами
     и lifecycle (`OnStart` — fail-fast подключение, `OnStop` — `Close`). Имя модуля
     сабпакета — `<kit>/<sub>`, параметризованного — `<kit>:<instance>`. Нужна
     fx-группа — заведи константу `<Thing>Group` со значением `<kit>.<thing>`
     (образцы: `healthkit.ProbesGroup`, `timerkit.JobsGroup`, `grpckit.ServicesGroup`) и
     рядом хелпер `Provide<Thing>(ctor, anns…)` с проверкой типа результата (значение —
     `Supply<Thing>(v)`); набор, где важен порядок, — одно значение графа, не группа.
     Конструктор — `(cfg, зависимости…, log, opts…)`, логгер — параметром, функция над готовым
     объектом берёт его первым (CLAUDE.md § Стиль «Одна форма API»).
   - `<kit>_test.go` — тесты (unit; testcontainers — через `testkit`, если нужен I/O;
     образы с конкретным тегом, никогда `:latest`).
   - `module_test.go` — fx-тест графа: `fx.ValidateApp(...)` с минимальным набором
     провайдеров (тот же, что в README кита).
   - Свои миграции (`pgkit.Source`, embed в `<kit>/pgstore/migrations`) — в `Module`
     `pgkit.SupplyMigrations(src)` и `pgkit.CheckSchemaOnStart(src)` (без миграций хранилище
     не стартует) и тест `TestMigrations_Ladder` на каждый источник:
     `testkit.MigrationLadder` проверяет, что down каждой версии возвращает схему к её up.
3. **Конфиг (если есть env-параметры).** Структура с `envconfig`-тегами — это
   **public API** (порядок тегов: `envconfig` первым, затем `default`).
   `NewConfig() (Config, error)` читает ENV ридером `internal/envconf` (`envconf.Load`,
   ошибка — `fmt.Errorf("%w: %w", ErrConfig, err)`; конструктор — строка в
   `internal/envconf/kits_test.go`) и вызывает `Config.Validate()`
   (sentinel `<kit>.ErrConfig`); `Validate` зовут и конструкторы `New*`, чтобы
   собранный литералом конфиг тоже проверялся. Zero-value `Config` либо безопасен
   и полезен, либо отвергается `Validate`. `Validate` — только проверка на value-приёмнике;
   ноль в литерале — значение из доки или `ErrConfig`, не дефолт кита; формат проверяется и
   у выключенного; опция с nil ничего не меняет. `Module` конфиг **не** provide'ит — его
   даёт caller. Каждый тег — строка таблицы в README кита (`| Env | Поле | Тип |
   Default | … |`; дефолт — как в `default`-теге, без него — `—` или нулевое
   значение), endpoint-переменная (`*_URL`, `*_DSN`, `*_ENDPOINT`, `*_ADDR`,
   `*_TARGET`) — ещё и в `README.md § Per-concern endpoints`; поле конфига без
   тега — только с `ignored:"true"` (`make envdoc-check`).
4. **Логирование и observability.** Логгер — `logkit.Logger` из fx-графа.
   Метрики/спаны — через `internal/otelkit` (`otelkit.Meter("<kit>")`,
   `otelkit.Must(...)`) в конструкторе, не в `var` пакета: инертны без провайдера
   у потребителя, пишут в провайдер, текущий на момент сборки. Имена —
   `<kit_prefix>_<what>_<unit|total>`, label'ы — конечные множества. Гистограмма
   длительности — `metric.WithUnit("s")` + `otelkit.DurationBuckets()`, своих
   границ не заводим. Кит с инфраструктурной зависимостью (БД, брокер, кэш,
   объектное хранилище) отдаёт `Probe(...) healthkit.Probe` (`healthkit.New`,
   при сетевом вызове — `healthkit.WithTimeout`) для `/ready`; для SaaS-зависимости
   проба opt-in — регистрирует потребитель.
5. **Ошибки и таймауты.** Sentinel-ошибки на английском с префиксом кита
   (`errors.New("<kit>: …")`, обёртка `fmt.Errorf("<kit>: …: %w", err)`; у сабпакета
   префикс — `<kit>/<sub>`); каждый исходящий вызов — с таймаутом (никакого
   `http.DefaultClient`); всё, что может быть отменено, принимает `ctx`.
6. **testkit-сабпакет (опц.).** Нужны helpers для тестов консьюмера — заведи
   `testkit/<name>test/`, где `<name>` — имя кита **без** суффикса `kit`
   (`cachekit` → `testkit/cachetest/`; см. `authntest`, `bruteforcetest`).
   Внутри каталога кита test-хелперов не держим; исключения — in-memory реализация
   двойного назначения, фейки часов `clockkit` и маркер `pgkit.WithTestTx` (CLAUDE.md
   § Стиль). Добавь строку в `testkit/README.md`
   и в перечень сабпакетов в корневом `README.md`.
7. **Документация.** Заведи `<kit>/README.md` (назначение, импорт, быстрый старт,
   конфигурация, observability, API, инварианты) — единый источник правды по
   киту. Для сложного кита разнеси детали по `<kit>/docs/` (`architecture.md`,
   `configuration.md`, `observability.md`, `usage.md`, `testing.md`). Плюс строка
   в `README.md § Пакеты` (и в env-таблицу, если кит читает endpoint) и в карту
   пакетов `CLAUDE.md`. Без README kit «невидим».
8. **Gate.** `make check` зелёный перед claim «готово»: integration-тесты кита
   (testcontainers) входят в него.

## Следующий шаг

После зелёного gate — релиз новой версии через `/release` (consumer'ы подтянут kit
через `go get @vX.Y.Z`; новый кит — добавление, значит minor). Новый сторонний
import в `go.mod` — сперва согласуй с пользователем.
