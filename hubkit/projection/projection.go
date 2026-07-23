// Package projection — эталон схемы локальных read-проекций хаба
// (00-architecture §4). Цель — идентичная схема companies/projects/employees/
// entity_visibility (+ курсор-таблица hub_sync_cursors) во всех продуктах, чтобы
// проекции не расходились.
//
// Это copy-paste-шаблон, а не рантайм: SQL встроен через embed для программного
// доступа (генераторы/тесты), но канонический способ — скопировать файлы
// templates/*.sql в каталог миграций продукта (адаптировав нумерацию под свою
// последовательность) и sqlc-запросы queries/*.sql — в свой sqlc-пакет.
//
// Проекции — read-only срез хаба: продукт наполняет их только через hubkit/sync
// (write-through идёт в hub API, не напрямую в проекцию). Надстройки продукта
// (kanban, борды, события) живут в отдельных таблицах поверх этих id.
package projection

import "embed"

// MigrationsFS — SQL-шаблоны миграций проекций (templates/*.sql).
//
//go:embed templates/*.sql
var MigrationsFS embed.FS

// QueriesFS — эталонные sqlc-запросы (queries/*.sql): upsert из фида + листинги.
//
//go:embed queries/*.sql
var QueriesFS embed.FS
