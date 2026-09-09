# mapkit

Декодеры значений из `map[string]any` — типичного результата `json.Unmarshal` в
untyped map (payload событий, конфиг-bag, динамический JSON). Каждая функция
читает по ключу, приводит тип и возвращает нулевой/`nil`-дефолт при отсутствии
ключа или несовпадении типа: без паники, без ошибки. Только stdlib, без I/O.

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

if lr := mapkit.IntPtr(p, "loss_reason_id"); lr != nil {
	// передано число (0 отличается от «не передано»/null)
	_ = *lr
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
func IntPtr(m map[string]any, k string) *int
func BoolPtr(m map[string]any, k string) *bool
```

## Observability

Кит **ничего не эмитит**: ни метрик, ни спанов, ни логов — это чистое чтение из
in-memory map.

## Поддерживаемые типы значений

| Функция | Принимает | Иначе |
|---|---|---|
| `String` | `string` | `""` |
| `Strings` | `[]any` (не-строковые элементы пропускаются), `[]string` (копия) | `nil` |
| `Float` | `float64`, `float32`, все `int*`/`uint*`, `json.Number` | `0` |
| `Int`, `Int64`, `IntPtr` | то же, что `Float`, если значение представимо в целевом типе | `0` / `nil` |
| `Map` | `map[string]any` | `nil` |
| `BoolPtr` | `bool` | `nil` |

`json.Unmarshal` кладёт в `map[string]any` только `string`, `float64`, `bool`,
`[]any`, `map[string]any` и `nil`; с `Decoder.UseNumber()` числа приходят
`json.Number`. Остальные типы поддержаны для map-литералов (тесты, адаптеры).

## Инварианты и подводные камни

- **Точность целых.** Без `UseNumber` число проходит через `float64`, поэтому
  целые больше 2^53 округляются ещё в `json.Unmarshal`:
  `9007199254740993` → `9007199254740992`. Нужны точные большие id —
  `Decoder.UseNumber()`: `Int64` читает `json.Number` через `Number.Int64()` без
  промежуточного `float64`.
- **Непредставимое значение = несовпадение типа.** Число вне диапазона целевого
  типа (`1e300`, `1e19`, `uint64` > `MaxInt64`), `NaN` и `±Inf` дают `0` для
  `Int`/`Int64` и `nil` для `IntPtr` — как если бы значение было не числом.
  Проверка `if id := mapkit.Int64(p, "lead_id"); id > 0` безопасна на любом
  payload'е.
- **Дробная часть усекается в сторону нуля.** `Int`/`Int64`/`IntPtr` от `2.999`
  дают `2`, от `-2.7` — `-2`. Округления нет; нужна дробная величина — `Float`.
- **`Float` отдаёт значение как есть.** Включая `NaN`/`±Inf` из map-литералов
  (в JSON таких литералов нет).
- **Дефолт скрывает «нет ключа» от «нулевого значения».** `String`/`Int`/`Float`
  не различают «ключа нет» и «значение пустое/0/не того типа» — все дают нулевой
  результат. Когда различие важно (0/`false` как валидное значение) —
  `IntPtr`/`BoolPtr`.
- **`IntPtr` и `BoolPtr` — по типу значения.** Указатель только если значение
  действительно число / `bool`; отсутствие ключа, `null` и другой тип дают
  `nil`. Так `{"price": 0}` даёт `*0`, а `{"price": null}` — `nil`.
- **`Strings` молча отбрасывает не-строки.** Элементы массива не-строкового типа
  пропускаются, поэтому «массив без строк» и «пустой массив» дают пустой
  не-`nil` срез; `nil` означает «ключа нет» или «значение не массив». Для
  `[]string` возвращается копия — изменение исходного среза не видно в
  результате.
- **`nil`-map безопасен.** Чтение из `nil`-map в Go даёт нулевое значение,
  поэтому все функции на `var m map[string]any` возвращают дефолты, а не паникуют.
