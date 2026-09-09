# flagkit

Простейшие feature flags из ENV. Одна переменная `FEATURE_FLAGS=foo=true,bar=baz`
разбирается в `Store`, который отдаёт значения по имени через `Bool`/`String`. Без
remote-источников и динамического refresh: набор флагов фиксируется при старте
процесса, изменение флага требует рестарта. Подходит для kill-switch'ей и
статических переключателей фич, которым не нужна живая перезагрузка.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/flagkit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

```go
package main

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/flagkit"
)

func main() {
	fx.New(
		// Config не входит в flagkit.Module — регистрируется отдельно.
		// Дешёвый mode без App-pattern:
		fx.Provide(flagkit.NewConfig),
		flagkit.Module, // provide'ит flagkit.Store (через NewEnvStore)
		fx.Invoke(func(flags flagkit.Store) {
			if flags.Bool("new_checkout") {
				// фича включена
			}
			region := flags.String("region") // сырое значение либо "" если флага нет
			_ = region
		}),
	).Run()
}
```

При `FEATURE_FLAGS=new_checkout=true,region=eu`:

- `flags.Bool("new_checkout")` → `true`
- `flags.String("region")` → `"eu"`
- `flags.Names()` → `["new_checkout", "region"]`
- `flags.Bool("unknown")` → `false` (флага нет)
- `flags.String("unknown")` → `""` (флага нет)

Точные сигнатуры:

```go
type Store interface {
	Bool(name string) bool
	String(name string) string
	Names() []string
}

func NewEnvStore(cfg Config) (*EnvStore, error)
func NewConfig() (Config, error)
func (c Config) Validate() error

var ErrConfig = errors.New("flagkit: invalid config")
```

`NewEnvStore` возвращает `*EnvStore` (реализует `Store`); вне fx его можно вызвать
напрямую, передав `Config` руками. Разбор строгий: невалидная запись — ошибка
`ErrConfig`, а не молча пропущенный флаг.

## Конфигурация

Единственное поле конфига:

| Env | Поле | Тип | Default | Правило `Validate` |
|---|---|---|---|---|
| `FEATURE_FLAGS` | `Config.Definitions` | `string` | `""` | Каждая непустая запись — `name=value` с непустым `name`; пусто — валидно |

`flagkit.Config` — **part of public API**. Имя поля `Definitions`, его тип и
envconfig-тег `FEATURE_FLAGS` менять = breaking change: выпускается
minor-релизом, помечается `!` в Conventional Commit и строкой миграции в
аннотации тега (см. [корневой README § Версионирование](../README.md)).

### Формат значения `FEATURE_FLAGS`

```
FEATURE_FLAGS=foo=true,bar=baz,booltrue=1
```

Правила разбора (`NewEnvStore`, `Config.Validate` — один и тот же парсер):

- Записи разделяются запятой `,`.
- В каждой записи имя и значение разделяет **первый** символ `=`; всё после него
  — значение (включая последующие `=`).
- Вокруг записи, имени и значения обрезаются пробелы (`strings.TrimSpace`).
- Пустая запись (подряд идущие запятые, запятая в конце) пропускается.
- Запись без `=` — ошибка `ErrConfig`.
- Запись с пустым именем (`=value`) — ошибка `ErrConfig`.
- Ошибки всех невалидных записей объединяются `errors.Join`: сообщение
  перечисляет каждую проблемную запись целиком.
- Пустая `FEATURE_FLAGS` (или незаданная переменная) даёт пустой `Store` — без
  ошибки.
- Имена **регистрозависимы**: `FOO=true` не отвечает на `Bool("foo")`.
- При повторе имени побеждает **последняя** запись: `foo=true,foo=false` →
  `Bool("foo") == false`.
- Кавычки не снимаются: `foo="true"` даёт `String("foo") == "\"true\""` и
  `Bool("foo") == false`.
- Разделитель `;` вместо `,` синтаксически неотличим от значения:
  `foo=true;bar=baz` — это один флаг `foo` со значением `true;bar=baz`, ошибки не
  будет. Разделяйте запятой.

## Observability

Кит **ничего не эмитит**: ни метрик, ни спанов, ни логов. Чтения флагов — это
доступ к in-memory map без I/O. Если нужно знать, какие флаги активны, логируйте
`Store.Names()` (или сырую `Config.Definitions`) на старте средствами
потребителя.

## API

| Символ | Роль | Когда использовать |
|---|---|---|
| `Store` | Интерфейс доступа к флагам (`Bool`, `String`, `Names`) | Зависимость в бизнес-коде — внедряется через fx |
| `Store.Bool(name)` | Булев флаг | Kill-switch / включение фичи: `true` только для значений `strconv.ParseBool` (`1`, `t`, `T`, `TRUE`, `true`, `True`); иначе и при отсутствии — `false` |
| `Store.String(name)` | Строковое значение | Когда флаг несёт не «вкл/выкл», а конфиг-значение (регион, режим, вариант) |
| `Store.Names()` | Имена заданных флагов, отсортированные | Лог активной конфигурации на старте |
| `EnvStore` | Реализация `Store` поверх разобранного `Config` | Конструируется через `NewEnvStore`; в fx скрыт за `Store` |
| `NewEnvStore(cfg)` | Конструктор `*EnvStore` | Прямое использование вне fx (передать `Config` руками) |
| `Config` | Конфиг с единственным полем `Definitions` | Заполняется из ENV через envconfig |
| `Config.Validate()` | Проверка формата без построения стора | Валидация App-конфига, собранного не через `NewConfig` |
| `NewConfig()` | Фабрика `Config` из ENV + `Validate` | Регистрация конфига в fx-графе (`Module` его не provide'ит) |
| `ErrConfig` | Sentinel невалидного `FEATURE_FLAGS` | `errors.Is(err, flagkit.ErrConfig)` в тестах и обработке ошибок старта |

`Bool` и `String` читают один и тот же разобранный набор: `String("foo")` для
`foo=true` вернёт сырую строку `"true"`, а `Bool("foo")` — `true`. Выбор метода —
по тому, как трактуется флаг, а не по тому, как он записан.

## Тестирование

Стор создаётся напрямую — без ENV и контейнеров. Передайте нужные флаги строкой
в `Config.Definitions`:

```go
store, err := flagkit.NewEnvStore(flagkit.Config{Definitions: "foo=true,bar=baz"})
if err != nil {
	t.Fatal(err)
}

store.Bool("foo")     // true
store.String("bar")   // "baz"
store.Bool("unknown") // false

// Пустой стор — все флаги выключены / пусты:
empty, _ := flagkit.NewEnvStore(flagkit.Config{})
empty.Bool("anything")   // false
empty.String("anything") // ""
```

Нулевое значение `flagkit.EnvStore{}` тоже пригодно как «все флаги выключены».
`*EnvStore` реализует `Store`, поэтому подставляется в код, который зависит от
`flagkit.Store`. Отдельный мок не нужен — `NewEnvStore` уже даёт детерминированный
стор; при желании потребитель может реализовать `Store` своим типом.

## Инварианты и подводные камни

- **Снимок на старте.** Флаги читаются один раз при конструировании `Store`.
  Изменение `FEATURE_FLAGS` применяется только после рестарта процесса — это не
  динамический feature-flag-сервис.
- **Строгий разбор.** Невалидная запись роняет `NewConfig`/`NewEnvStore` с
  `ErrConfig` — опечатка в переменной деплоя останавливает старт, а не выключает
  kill-switch молча. Опечатка в *имени* флага при этом не диагностируется:
  неизвестное имя всегда даёт дефолт.
- **Дефолт при отсутствии флага.** Неизвестное имя — это `false` для `Bool` и
  `""` для `String`. Стор не различает «флаг задан со значением `false`» и «флага
  нет»: оба дают `Bool == false`; различить помогает `Names()`.
- **Семантика `Bool`.** Истинны только значения, которые принимает
  `strconv.ParseBool`: `1`, `t`, `T`, `TRUE`, `true`, `True`. Любое другое
  (`yes`, `on`, `TrUe`, `0`, пустое, произвольный текст) — `false`. Значение
  `baz` для `bar` означает `Bool("bar") == false`, но `String("bar") == "baz"`.
- **Типизация — на стороне читателя.** Стор хранит только строки. Числа, enum'ы,
  списки и т.п. возвращаются через `String` и парсятся потребителем.
- **Стор иммутабелен.** Map заполняется в конструкторе и дальше только читается,
  поэтому конкурентные `Bool`/`String`/`Names` безопасны без синхронизации.
- **`Module` не provide'ит `Config`.** Однородно с остальными китами: фабрика
  `NewConfig` экспортирована, но в `Module` не входит — регистрируйте конфиг сами
  (через App-структуру или `fx.Provide(flagkit.NewConfig)`).
