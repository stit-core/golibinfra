# Roadmap

Единственный артефакт планирования — этот файл. Подпапок, `<slug>.md`, архивов и
changelog'ов нет: они расходятся с правдой и мешают.

**Planning-first.** Изменение идёт через план — `### <slug>`-секцию под `## Active` ниже.
Прямая правка без плана допустима только для truly-atomic (опечатка, один очевидный файл) и
багфиксов (RED-first). Отклонение от плана — осознанное, проговаривается с юзером.

**Жизненный цикл.** `planning → in-progress → blocked? → done`. По `done` секция удаляется
целиком — история остаётся в `git log`. Незакрытый follow-up переезжает в `## Backlog` или
новой `### `-секцией, а не хвостом в удаляемой.

**Где это запускается.** Репо — библиотека, живого стека нет: гейт локальный — `make check`
(fmt, vet, lint, test -short -race, build, tidy, govulncheck, envdoc), перед тегом —
`make gate-clean` (то же в чистом worktree). Выпуск версии — тег `vX.Y.Z` (skill `/release`),
консьюмеры подтягивают `go get` сами.

## Active

_(пусто)_

## Backlog

- **Релиз breaking-minor и миграция потребителей.** Следующий тег — minor с `!`; в
  аннотации перечислить breaking по китам. Потребители (`go-backend-template`,
  `ai-gateway-template`, `amocrm-gateway-template`) правят: `logkit.NewWithWriter`
  возвращает `(…, error)`; `natskit.Subscribe*JSON` → `(Subscription, error)`;
  `outboxkit.New(name, repo, transport, transactor, cfg)` и
  `RegisterBacklogGauges(instance, reporter)`; миграции — `pgkit.Source{Name, FS, Dir}`,
  `pgkit.MigrateUpAll`/`MigrateModule` с группой `pgkit.migrations` вместо
  `MultiSourceConfig`/`MigrateUpMulti`, `pgstore.Migrations` вместо `MigrationsFS`;
  `amocrmkit.New(cfg, httpclient.Config, …)`. Существующие БД: таблица версий
  `schema_migrations` → `schema_migrations_app` (rename перед деплоем), для китов —
  `schema_migrations_<kit>`.
- **Драйвер миграций `database/pgx/v5`.** Требует новый модуль `github.com/jackc/pgerrcode`
  (решение о зависимости — за оператором); после `go get` — две строки в `pgkit/migrate.go`,
  `lib/pq` уходит из графа целиком.
- **`ANTHROPIC_DEFAULT_MODEL`.** Дефолт `claude-opus-4-8`; смена на `claude-opus-5` —
  решение оператора (меняет дефолтную стоимость вызовов у потребителей).
- **CI на Gitea Actions**, когда появится раннер: `make check` + `make test-full`.
- **Вынос `hubkit` (SDK хаба) и `amocrmkit`** в отдельные модули; триггер — второй
  потребитель вне stit-core или конфликт версий зависимостей у потребителя.
- **`hubkit/projection`:** синхронизировать схему с клиентом (departments, tags,
  entity-tags) или перенести в шаблон продукта.
