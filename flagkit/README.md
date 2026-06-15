# flagkit

Простейшие feature flags из ENV. Одна переменная `FEATURE_FLAGS=foo=true,bar=baz`
парсится в `Store`, который отдаёт значения по имени через `Bool`/`String`. Без
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
- `flags.Bool("unknown")` → `false` (флага нет)
- `flags.String("unknown")` → `""` (флага нет)

Точные сигнатуры:

```go
type Store interface {
	Bool(name string) bool
	String(name string) string
}

func NewEnvStore(cfg Config) *EnvStore
func NewConfig() (Config, error)
```

`NewEnvStore` возвращает `*EnvStore` (реализует `Store`); вне fx его можно вызвать
напрямую, передав `Config` руками.

## Конфигурация

Единственное поле конфига:

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `FEATURE_FLAGS` | `Config.Definitions` | `string` | `""` | Список флагов в формате `key=value`, разделённых запятой |

`flagkit.Config` — **part of public API** (см. [корневой README § Стабильность
API](../README.md)). Имя поля `Definitions`, его тип и envconfig-тег
`FEATURE_FLAGS` менять = breaking change → мажорный апгрейд.

### Формат значения `FEATURE_FLAGS`

```
FEATURE_FLAGS=foo=true,bar=baz,booltrue=1
```

Правила парсинга (`NewEnvStore`):

- Записи разделяются запятой `,`.
- В каждой записи ключ и значение разделяет **первый** символ `=`; всё после него
  — значение (включая последующие `=`).
- Вокруг записи, ключа и значения обрезаются пробелы (`strings.TrimSpace`).
- Запись без `=` молча пропускается.
- Запись с пустым ключом молча пропускается.
- Пустая `FEATURE_FLAGS` (или незаданная переменная) даёт пустой `Store` — без
  ошибки.

## Observability

Кит **ничего не эмитит**: ни метрик, ни спанов, ни логов. Чтения флагов — это
доступ к in-memory map без I/O. Если нужно знать, какие флаги активны, логируйте
их на старте приложения средствами потребителя.

## API

| Символ | Роль | Когда использовать |
|---|---|---|
| `Store` | Интерфейс доступа к флагам (`Bool`, `String`) | Зависимость в бизнес-коде — внедряется через fx |
| `Store.Bool(name)` | Булев флаг | Kill-switch / включение фичи: `true` только если значение `true` (без учёта регистра) или `1`; иначе и при отсутствии — `false` |
| `Store.String(name)` | Строковое значение | Когда флаг несёт не «вкл/выкл», а конфиг-значение (регион, режим, вариант) |
| `EnvStore` | Реализация `Store` поверх распарсенного `Config` | Конструируется через `NewEnvStore`; в fx скрыт за `Store` |
| `NewEnvStore(cfg)` | Конструктор `*EnvStore` | Прямое использование вне fx (передать `Config` руками) |
| `Config` | Конфиг с единственным полем `Definitions` | Заполняется из ENV через envconfig |
| `NewConfig()` | Фабрика `Config` через `envconfig.Process` | Регистрация конфига в fx-графе (`Module` его не provide'ит) |

`Bool` и `String` читают один и тот же распарсенный набор: `String("foo")` для
`foo=true` вернёт сырую строку `"true"`, а `Bool("foo")` — `true`. Выбор метода —
по тому, как трактуется флаг, а не по тому, как он записан.

## Тестирование

Стор создаётся напрямую — без ENV и контейнеров. Передайте нужные флаги строкой
в `Config.Definitions`:

```go
store := flagkit.NewEnvStore(flagkit.Config{Definitions: "foo=true,bar=baz"})

store.Bool("foo")     // true
store.String("bar")   // "baz"
store.Bool("unknown") // false

// Пустой стор — все флаги выключены / пусты:
empty := flagkit.NewEnvStore(flagkit.Config{})
empty.Bool("anything")   // false
empty.String("anything") // ""
```

`*EnvStore` реализует `Store`, поэтому подставляется в код, который зависит от
`flagkit.Store`. Отдельный мок не нужен — `NewEnvStore` уже даёт детерминированный
стор; при желании потребитель может реализовать `Store` своим типом.

## Инварианты и подводные камни

- **Снимок на старте.** Флаги читаются один раз при конструировании `Store`.
  Изменение `FEATURE_FLAGS` применяется только после рестарта процесса — это не
  динамический feature-flag-сервис.
- **Дефолт при отсутствии флага.** Неизвестное имя — это `false` для `Bool` и
  `""` для `String`. Стор не различает «флаг задан со значением `false`» и «флага
  нет»: оба дают `Bool == false`.
- **Семантика `Bool`.** Истинными считаются только `true` (любой регистр) и `1`.
  Любое другое значение (`yes`, `on`, `false`, `0`, пустое, произвольный текст) —
  `false`. Значение `baz` для `bar` означает `Bool("bar") == false`, но
  `String("bar") == "baz"`.
- **Типизация — на стороне читателя.** Стор хранит только строки. Числа, enum'ы,
  списки и т.п. возвращаются через `String` и парсятся потребителем.
- **Тихий парсинг.** Невалидные записи (без `=`, с пустым ключом) отбрасываются
  без ошибки или лога. Опечатка в имени флага не диагностируется — `Bool`/`String`
  по такому имени просто вернут дефолт.
- **`Module` не provide'ит `Config`.** Однородно с остальными китами: фабрика
  `NewConfig` экспортирована, но в `Module` не входит — регистрируйте конфиг сами
  (через App-структуру или `fx.Provide(flagkit.NewConfig)`).
