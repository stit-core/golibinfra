# archkit

Машинные правила раскладки сервиса на golibinfra. Сервисы stit делят одну раскладку —
`internal/domain/<d>` со слоями, `internal/platform`, `pkg`, `cmd`; кит держит её границы одним
вызовом из теста потребителя: каталоги `internal/` и пакеты `pkg/`, импорты между доменами и слоями,
корень домена без транспорта и I/O, чистые слои, настенные часы, порты между доменами, свои таблицы.

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
		archkit.WithInternalDirs("internal/archtest"),
		archkit.WithRootLibs(
			"git.stit.tech/stit-core/golibinfra/clockkit",
			"git.stit.tech/stit-core/golibinfra/logkit",
			"git.stit.tech/stit-core/golibinfra/pgkit",
			"go.uber.org/fx",
		),
	)
}
```

`WithInternalDirs` называет каталог самого теста, `WithRootLibs` — внешние пакеты, которые видит
корень домена; пакеты `pkg/` сервиса называет `WithPkgPackages`, остальное — по коду сервиса, § Настройки
потребителя.

Нарушение — строка отказа теста `archkit: <нарушение>: <правило>`:

```
archkit: internal/domain/order/crossref.go imports internal/domain/customer: another domain is reached through a port and an adapter in module/
```

## Раскладка

```
cmd/<bin>/                        входы; cmd/migrate — миграции, в том числе outbox доменов
migrations/*.up.sql               таблицы приложения
pkg/<p>/                          правила и типы, общие доменам: пакеты WithPkgPackages
internal/platform/<p>/            клей платформы
internal/<своё>/                  каталоги потребителя (WithInternalDirs)
internal/domain/<d>/              корень домена: usecase, порты; чистые слои — entity.go, policy.go,
                                  dto.go, errors.go, events.go
internal/domain/<d>/delivery/…    входы домена
internal/domain/<d>/infra/…       адаптеры: infra/postgres (queries/*.sql, db — вывод sqlc), infra/outbox
internal/domain/<d>/module/       fx-модуль и адаптеры портов к другим доменам
internal/domain/<d>/mocks/        моки портов (и в подпакетах)
```

Образец раскладки — `testdata/clean` (фикстура тестов кита: правила её читают, гейт не собирает).

## Правила

Правила читают пакеты шаблона `./...` — без каталогов `testdata`, `vendor`, имён на `.` и `_` и
вложенных модулей — и пакеты модуля, которые этот код и его тесты импортируют, где бы они ни
лежали (и в каталоге-ссылке): сборка берёт пакет по импорту. Код таких каталогов, который никто не
импортирует, правила слоёв не читают — в сборке его нет. Go-файл-ссылка читается как файл.
`*_test.go` проверяют только правила каталогов `internal/` и пакетов `pkg/`. Пути в отказах — от корня модуля; одно нарушение —
одна строка.

| Правило | Что держит |
|---|---|
| Каталоги `internal/` | Go-файл под `internal/` лежит в `internal/domain`, `internal/platform` или каталоге `WithInternalDirs`: вне них код не видят правила слоёв. Правило смотрит, где лежит код, а не что из него собрано, — и в `testdata`, каталоге на `.` или `_`, вложенном модуле |
| Пакеты `pkg/` | Go-файл под `pkg/` лежит в пакете `WithPkgPackages`: новый пакет — запись в настройках, видимая в диффе, а generic-примитив — в golibinfra. Правило смотрит, где лежит код, — и в `testdata`, каталоге на `.` или `_`, вложенном модуле, каталоге-ссылке, который сборка берёт по импорту |
| Границы импортов | Не-тестовый код модуля импортирует пакеты модуля по таблице ниже |
| Чистые слои | `entity.go`, `policy.go`, `dto.go`, `errors.go`, `events.go` корня домена видят stdlib без побочных эффектов и потоков (без `database/sql`, `net`, `os`, `syscall`, `io`, `bufio` и пакетов под ними; парсеры `net/mail`, `net/netip`, `net/url` — можно) и пакеты `WithPurePackages` |
| Корень домена | Файлы корня видят stdlib без I/O (без `database/sql`, `net`, `os`, `syscall` и пакетов под ними, кроме тех же парсеров), пакеты `WithRootLibs`, а из `pkg/` и `internal/platform/` — пакеты, которые сами видят только это (пакеты `WithPurePackages` — на том же условии), и пакеты `WithUncheckedPackages`; путь до I/O — в скобках отказа: `(pkg/wrap → internal/platform/probeio → net/http)` |
| Часы | Не-тестовый код домена не трогает `time.Now`, `Since`, `Until`, `After`, `AfterFunc`, `Tick`, `NewTimer`, `NewTicker`, `Sleep` (и под алиасом) и не dot-импортирует `time`: время — `clockkit.Clock` |
| Порты между доменами | В `module/` нет функции, которая возвращает свой параметр чужого домена под типом не из чужого домена — порт без адаптера: `func(uc *user.Usecase) auth.Port { return uc }`. Любой `return` и любая позиция результата; параметр — как есть, в скобках, приведением к интерфейсу или к чужому типу (`auth.Port(uc)`), утверждением типа (и `v, ok := x.(T)`), через переменную или именованный результат, из литерала функции, который его захватил; тип параметра — и под алиасом `module/` или dot-импортом домена. Приведение к своему или внешнему типу (`auth.Status(s)`, `string(s)`) — новое значение, перевод типов адаптером |
| Свои таблицы | Таблицу из `migrations/*.up.sql` (`CREATE TABLE`, и `UNLOGGED` или `TEMP`, любой схемы, и в блоке `DO`) запрашивает один домен (`infra/postgres/queries/*.sql`): `FROM`, `JOIN`, список через запятую, подзапрос, скобки соединения `(a JOIN b)`, `INSERT`, `UPDATE`, `DELETE … USING`. Имя CTE своего оператора — не таблица (`WITH users AS (…) SELECT … FROM users`), в теле CTE без `RECURSIVE` — таблица. Представление и функция (процедура) из миграций — обращение к таблицам, которые читает их определение (тело `$$…$$`, `'…'`, `RETURN …`, `BEGIN ATOMIC … END`, и цепочкой через другие представления и функции); отказ называет обход: `(order through view active_customers)` |

Границы импортов, первая подходящая строка:

| Кто → что | Отказ |
|---|---|
| любой не-тестовый код → пакет `mocks` | `mocks are for tests only` |
| `internal/platform`, `pkg` → всё, кроме них самих | `platform and pkg lie below the domains: they import only platform and pkg` |
| домен → пакет модуля вне доменов, `platform` и `pkg`: `cmd`, каталог `WithInternalDirs`, пакет в корне модуля (`.`) | `besides domains, a domain imports only platform and pkg: …` |
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
| `WithPurePackages(pkgs…)` | Пакеты модуля (путь от корня: `pkg/policy`), которые видят чистые слои. Корень домена проверяет их импорты наравне с остальными пакетами `pkg/` и `internal/platform/` |
| `WithUncheckedPackages(pkgs…)` | Исключение из правила корня: пакеты `pkg/` и `internal/platform/` (путь от корня: `internal/platform/identity`), в импорты которых правило не заглядывает. За чистоту такого пакета отвечает потребитель, машина её не держит. Подпакет под запись не попадает. Запись про запас — отказ: о пакете, который чист и без неё, о пакете, которого корень домена не импортирует ни сам, ни через пакеты этих каталогов, и мимо этих каталогов. Предел: запись о пакете с I/O под другим доверенным пакетом с I/O (корень → `pkg/a` → `pkg/b`) принимается, хотя при записи о верхнем ничего не меняет |
| `WithInternalDirs(dirs…)` | Каталоги `internal/` сверх `domain` и `platform` (путь от корня: `internal/testkit`). Другая форма (`internal`, `testkit`, `internal/a/b`) — отказ |
| `WithPkgPackages(pkgs…)` | Пакеты `pkg/` сервиса (путь от корня: `pkg/policy`); без записи пакет `pkg/` — нарушение. Подпакет под запись не попадает — у него своя. Запись о каталоге без Go-файлов — отказ, другая форма (`pkg`, `policy`, `pkg/policy/`) — тоже |
| `WithKnownViolations(map)` | Известные нарушения — см. ниже |

Опции складываются; `nil` и пустой список ничего не меняют. Пути пакетов модуля — без module path,
поэтому настройки не меняются при его смене.

Сервис, у которого `internal/platform/identity` берёт IP клиента из httpkit, а личность и
`pkg/policy` видят чистые слои, дописывает к вызову быстрого старта:

```go
archkit.WithPkgPackages("pkg/policy"),
archkit.WithPurePackages("internal/platform/identity", "pkg/policy"),
archkit.WithUncheckedPackages("internal/platform/identity"),
```

Запись `WithUncheckedPackages` — только если такой пакет есть и его берёт корень домена: запись про
запас — отказ (случаи — в таблице выше), и у сервиса без этого пакета тот же вызов красный:

```
archkit: unchecked package "internal/platform/identity" sees nothing the domain root does not: remove it from WithUncheckedPackages
```

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
func WithUncheckedPackages(pkgs ...string) Option
func WithInternalDirs(dirs ...string) Option
func WithPkgPackages(pkgs ...string) Option
func WithKnownViolations(known map[string]string) Option
```

`Check` — единственная точка входа: нарушения — `t.Errorf`, по строке на нарушение.

## Инварианты и подводные камни

- **Дерево, которое правила не видят, — отказ.** Нет `go.mod` или строки `module`, нет Go-пакетов
  в `internal/domain`, Go-файл не разбирается, у доменов есть запросы, а `migrations/*.up.sql` не
  создают таблиц — `t.Fatalf`: зелёный прогон значит, что правила проверили код.
- **Правило своих таблиц читает запросы sqlc и объявления миграций.** Нет
  `infra/postgres/queries/*.sql` ни у одного домена — правилу нечего проверять, есть миграции или нет.
  SQL в Go-коде, таблицы китов (их нет в `migrations/`), динамический SQL (`EXECUTE` в теле функции),
  триггеры и правила (`CREATE TRIGGER`, `CREATE RULE`), секцию чужой таблицы (`PARTITION OF`) и объект
  под новым именем (`ALTER … RENAME TO`) оно не видит; определения представления или функции из разных
  миграций складываются — `DROP` и `OR REPLACE` их не вычитают.
- **Правила синтаксические.** Импорт — по тексту файла, вне доменов файл разбирается до конца
  импортов; порт между доменами — по функции `module/`, которая возвращает свой параметр: типы — по
  объявлениям пакетов доменов (алиас, dot-импорт, интерфейс), путь значения — по тексту тела; запросы — по
  словам в позиции таблицы: SQL режется на слова, строки и комментарии, грамматику правило не разбирает.
  То, что требует типов или смысла (логика в адаптере, встраивание чужого типа, значение из поля
  параметра, функции-помощника, составного литерала или переменной пакета, приведение к интерфейсу вне
  пакетов доменов — `fmt.Stringer(uc)`), держит ревью.
- **Цепочка до I/O идёт по не-тестовым файлам** пакетов `pkg/` и `internal/platform/`; тест пакета
  может ходить в сеть.
- **Новое или более строгое правило — minor-версия golibinfra** со строкой `Breaking` в аннотации
  тега: что стало нарушением и чем его закрыть (правкой кода или записью `WithKnownViolations`).
