# mapkit

Декодеры значений из `map[string]any` — типичного результата `json.Unmarshal` в
untyped map (payload событий, конфиг-bag, динамический JSON). Каждая функция
читает по ключу, приводит тип и возвращает нулевой/`nil`-дефолт при отсутствии
ключа или несовпадении типа: без паники, без ошибки. Без зависимостей и I/O.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/mapkit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

```go
var p map[string]any
_ = json.Unmarshal(raw, &p)

name  := mapkit.String(p, "name")          // "" если нет/не строка
price := mapkit.Int(p, "price")            // 0 если нет/не число
tags  := mapkit.Strings(p, "tags")         // nil, не-строки отброшены
sub   := mapkit.Map(p, "nested")           // nil если нет/не объект

if lr, ok := mapkit.IntPtr(p, "loss_reason_id"); ok {
	// ключ передан (0 отличается от «не передано»)
	_ = lr
}
```

Точные сигнатуры:

```go
func String(m map[string]any, k string) string
func Strings(m map[string]any, k string) []string
func Float(m map[string]any, k string) float64
func Int(m map[string]any, k string) int
func Int64(m map[string]any, k string) int64
func Map(m map[string]any, k string) map[string]any
func IntPtr(m map[string]any, k string) (*int, bool)
func BoolPtr(m map[string]any, k string) *bool
```

## Observability

Кит **ничего не эмитит**: ни метрик, ни спанов, ни логов — это чистое чтение из
in-memory map.

## Инварианты и подводные камни

- **JSON-числа — это `float64`.** `json.Unmarshal` кладёт все числа как `float64`;
  `Int`/`Int64` усекают `Float` до целого. `int`/`int64` поддержаны дополнительно
  для прямых map-литералов (тесты), но в распарсенном JSON их не будет.
- **Дефолт скрывает «нет ключа» от «нулевого значения».** `String`/`Int`/`Float`
  не различают «ключа нет» и «значение пустое/0/не того типа» — все дают нулевой
  результат. Когда различие важно (0 как валидное значение) — `IntPtr`: он
  смотрит на наличие ключа, а не на значение.
- **`BoolPtr` — по типу, не по наличию.** Возвращает указатель только если
  значение действительно `bool`; отсутствие ключа и не-bool оба дают `nil`.
- **`Strings` молча отбрасывает не-строки.** Элементы массива не-строкового типа
  пропускаются; не-массив даёт `nil`.
