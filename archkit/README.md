# archkit

Машинные правила раскладки сервиса на golibinfra. Сервисы stit делят одну раскладку —
`internal/domain/<d>` со слоями, `internal/platform`, `pkg`, `cmd`; кит держит её границы одним
вызовом из теста потребителя: каталоги `internal/`, импорты между доменами и слоями, корень
домена без транспорта и I/O, чистые слои, настенные часы, порты между доменами, свои таблицы.

Правила едут в сервис версией golibinfra; у сервиса остаются настройки и список известных
нарушений. Импортируется только из `*_test.go` — в production-граф не попадает.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/archkit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

Один тест в любом пакете модуля — корень и module path кит берёт из ближайшего `go.mod` вверх от
каталога теста:

```go
package archtest

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/archkit"
)

func TestLayout(t *testing.T) {
	t.Parallel()
	archkit.Check(
		t,
		archkit.WithInternalDirs("internal/archtest", "internal/testkit"),
		archkit.WithPurePackages("internal/platform/identity", "pkg/policy"),
		archkit.WithRootLibs(
			"git.stit.tech/stit-core/golibinfra/clockkit",
			"git.stit.tech/stit-core/golibinfra/logkit",
			"git.stit.tech/stit-core/golibinfra/pgkit",
			"go.uber.org/fx",
		),
	)
}
```

Нарушение — строка отказа теста `archkit: <нарушение>: <правило>`:

```
archkit: internal/domain/order/crossref.go imports internal/domain/customer: another domain is reached through a port and an adapter in module/
```

## Раскладка

```
cmd/<bin>/                        входы; cmd/migrate — миграции, в том числе outbox доменов
migrations/*.up.sql               таблицы приложения
pkg/<p>/                          правила и типы, общие доменам
internal/platform/<p>/            клей платформы
internal/<своё>/                  каталоги потребителя (WithInternalDirs)
internal/domain/<d>/              корень домена: usecase, порты; чистые слои — entity.go, policy.go,
                                  dto.go, errors.go, events.go
internal/domain/<d>/delivery/…    входы домена
internal/domain/<d>/infra/…       адаптеры: infra/postgres (queries/*.sql, db — вывод sqlc), infra/outbox
internal/domain/<d>/module/       fx-модуль и адаптеры портов к другим доменам
internal/domain/<d>/mocks/        моки портов (и в подпакетах)
```

Образец чистого сервиса — `testdata/clean`.

## Правила

Правила читают те же Go-файлы, что `go build`: без каталогов `testdata`, `vendor`, имён на `.` и
`_` и вложенных модулей. `*_test.go` проверяет только правило каталогов. Пути в отказах — от корня
модуля; одно нарушение — одна строка.

| Правило | Что держит |
|---|---|
| Каталоги `internal/` | Пакет `internal/` лежит в `internal/domain`, `internal/platform` или каталоге `WithInternalDirs`: вне них код не видят правила слоёв |
| Границы импортов | Не-тестовый код модуля импортирует пакеты модуля по таблице ниже |
| Чистые слои | `entity.go`, `policy.go`, `dto.go`, `errors.go`, `events.go` корня домена видят stdlib без побочных эффектов и потоков (без `database/sql`, `net`, `os`, `syscall`, `io`, `bufio` и пакетов под ними; парсеры `net/mail`, `net/netip`, `net/url` — можно) и пакеты `WithPurePackages` |
| Корень домена | Остальные файлы корня видят stdlib без I/O (без `database/sql`, `net`, `os`, `syscall` и пакетов под ними, кроме тех же парсеров), пакеты `WithRootLibs` и `WithPurePackages`, а из `pkg/` и `internal/platform/` — пакеты, которые сами видят только это; путь до I/O — в скобках отказа: `(pkg/wrap → internal/platform/probeio → net/http)` |
| Часы | Не-тестовый код домена не трогает `time.Now`, `Since`, `Until`, `After`, `AfterFunc`, `Tick`, `NewTimer`, `NewTicker`, `Sleep` (и под алиасом) и не dot-импортирует `time`: время — `clockkit.Clock` |
| Порты между доменами | В `module/` нет функции, которая отдаёт значение чужого домена как порт без адаптера: `func(uc *user.Usecase) auth.Port { return uc }`, и в форме `(auth.Port, error)`, и с другими параметрами рядом |
| Свои таблицы | Таблицу из `migrations/*.up.sql` запрашивает один домен (`infra/postgres/queries/*.sql`): `FROM`, `JOIN`, список через запятую, подзапрос, `INSERT`, `UPDATE`, `DELETE … USING` |

Границы импортов, первая подходящая строка:

| Кто → что | Отказ |
|---|---|
| любой не-тестовый код → пакет `mocks` | `mocks are for tests only` |
| `internal/platform`, `pkg` → всё, кроме них самих | `platform and pkg lie below the domains: they import only platform and pkg` |
| домен → пакет модуля вне доменов, `platform` и `pkg`: `cmd`, каталог `WithInternalDirs` | `besides domains, a domain imports only platform and pkg: …` |
| кто-либо, кроме `module/` домена и `cmd/migrate`, → `infra/outbox` домена | `a domain outbox is mounted by its own module/ and migrated by cmd/migrate only` |
| кто-либо, кроме `infra/postgres` домена, → его `infra/postgres/db` | `sqlc types stay inside the domain's infra/postgres` |
| домен, кроме `module/`, → другой домен | `another domain is reached through a port and an adapter in module/` |
| `module/` → подпакет другого домена | `module/ reaches another domain through its root package only` |
| корень домена → свой подпакет | `the domain root depends on ports, not on its own subpackages` |
| `delivery/…` → `infra/…` своего домена | `delivery calls the usecase, not infra` |

`cmd/`, каталоги `WithInternalDirs` и прочие пакеты вне `internal/` и `pkg/` лежат над доменами:
импортируют что угодно, кроме моков, outbox доменов (он — у `cmd/migrate`) и sqlc-типов.

## Настройки потребителя

| Опция | Значение |
|---|---|
| `WithRootLibs(pkgs…)` | Внешние пакеты, которые видит корень домена: порты и значения китов, DI. Путь импорта целиком — подпакет (`auditkit/pgstore` при `auditkit`) под запись не попадает |
| `WithPurePackages(pkgs…)` | Пакеты модуля (путь от корня: `internal/platform/identity`), которые видят чистые слои. Корень домена видит их, не заглядывая в их импорты, — за чистоту такого пакета отвечает потребитель |
| `WithInternalDirs(dirs…)` | Каталоги `internal/` сверх `domain` и `platform` (путь от корня: `internal/testkit`). Другая форма (`internal`, `testkit`, `internal/a/b`) — отказ |
| `WithKnownViolations(map)` | Известные нарушения — см. ниже |

Опции складываются; `nil` и пустой список ничего не меняют. Пути пакетов модуля — без module path,
поэтому настройки не меняются при его смене.

## Известные нарушения

Новое правило на живом коде находит долг. `WithKnownViolations` держит гейт зелёным, а долг —
видимым и конечным: ключ — нарушение из строки отказа (текст между `archkit: ` и первым `: `),
значение — ID находки аудита или задачи трекера (`ADP-02`, `PROJ-1234`).

```go
archkit.WithKnownViolations(map[string]string{
	"internal/domain/auth/module/module.go returns *user.Usecase as auth.PasswordVerifier": "ADP-02",
})
```

Отказ — и запись без такого ID, и запись, которой не отвечает нарушение: исправленное
вычёркивается.

## Конфигурация

ENV кит не читает: настройки — опции `Check`.

## Observability

Кит **ничего не эмитит**: ни метрик, ни спанов, ни логов.

## API

```go
func Check(t testing.TB, opts ...Option)

type Option func(*options)

func WithRootLibs(pkgs ...string) Option
func WithPurePackages(pkgs ...string) Option
func WithInternalDirs(dirs ...string) Option
func WithKnownViolations(known map[string]string) Option
```

`Check` — единственная точка входа: нарушения — `t.Errorf`, по строке на нарушение.

## Инварианты и подводные камни

- **Дерево, которое правила не видят, — отказ.** Нет `go.mod` или строки `module`, нет Go-пакетов
  в `internal/domain`, Go-файл не разбирается, у доменов есть запросы, а `migrations/*.up.sql` не
  создают таблиц — `t.Fatalf`: зелёный прогон значит, что правила проверили код.
- **Сервис без БД** (нет ни миграций, ни запросов) правило своих таблиц не затрагивает. Таблицы
  китов (их нет в `migrations/`) вне правила.
- **Правила синтаксические.** Импорт — по тексту файла, вне доменов файл разбирается до конца
  импортов; порт между доменами узнаётся по форме функции (тело — `return` её параметра); запросы — по словам в позиции таблицы, без разбора SQL. То, что требует типов или
  смысла (логика в адаптере, встраивание чужого типа), держит ревью.
- **Цепочка до I/O идёт по не-тестовым файлам** пакетов `pkg/` и `internal/platform/`; тест пакета
  может ходить в сеть.
- **Новое или более строгое правило — minor-версия golibinfra** со строкой `Breaking` в аннотации
  тега: что стало нарушением и чем его закрыть (правкой кода или записью `WithKnownViolations`).
