// Package envconf читает переменные окружения в структуру по тегам envconfig, default,
// required и ignored — один ридер конфигов китов и config.Env.
package envconf

import (
	"encoding"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Empty — значение переменной, которое явно задаёт строке, списку или типу со своим разбором
// пустое значение: пустая переменная равна незаданной и берёт default.
const Empty = "-"

// Decoder — тип со своим разбором значения переменной.
type Decoder interface {
	Decode(value string) error
}

var (
	durationType        = reflect.TypeFor[time.Duration]()
	decoderType         = reflect.TypeFor[Decoder]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// Load заполняет dst — указатель на структуру — переменными с именами тегов envconfig.
func Load(dst any) error { return LoadPrefixed(dst, "", false) }

// LoadPrefixed читает переменные <PREFIX>_<тег>; с fallback незаданная берётся из голого
// <тег>. Вложенная структура без тега читается с тем же префиксом; тег у вложенной структуры
// и другое поле без тега и без ignored:"true" — ошибка с именем поля. Пустое значение равно
// незаданной переменной: default, а без него поле не меняется. Ошибка значения называет
// переменную.
func LoadPrefixed(dst any, prefix string, fallback bool) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("want a non-nil pointer to struct, got %T", dst)
	}
	if prefix != "" {
		prefix = strings.ToUpper(prefix) + "_"
	}
	return loadStruct(rv.Elem(), prefix, fallback)
}

func loadStruct(sv reflect.Value, prefix string, fallback bool) error {
	st := sv.Type()
	for i := range st.NumField() {
		meta := st.Field(i)
		if !meta.IsExported() || meta.Tag.Get("ignored") == "true" {
			continue
		}
		field := sv.Field(i)
		tagged := meta.Tag.Get("envconfig") != ""
		var err error
		switch {
		case tagged && nestedStruct(meta.Type):
			// Тег не даёт вложенной структуре приставку: имена у неё — общие с родителем.
			err = fmt.Errorf("field %s.%s: envconfig tag on a nested struct: remove it, nested struct fields share the prefix", st.Name(), meta.Name)
		case tagged:
			err = loadField(field, meta, prefix, fallback)
		case nestedStruct(meta.Type):
			err = loadStruct(structValue(field), prefix, fallback)
		default:
			err = fmt.Errorf("field %s.%s has no envconfig tag: tag it or mark it ignored:\"true\"", st.Name(), meta.Name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// nestedStruct — структура (или указатель на неё) без своего разбора: её читают поля.
func nestedStruct(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct && !decodes(t)
}

// structValue — значение вложенной структуры; nil-указатель получает новую структуру.
func structValue(field reflect.Value) reflect.Value {
	if field.Kind() != reflect.Pointer {
		return field
	}
	if field.IsNil() {
		field.Set(reflect.New(field.Type().Elem()))
	}
	return field.Elem()
}

func loadField(field reflect.Value, meta reflect.StructField, prefix string, fallback bool) error {
	tag := strings.ToUpper(meta.Tag.Get("envconfig"))
	key := prefix + tag
	raw, set := lookup(key)
	if !set && fallback && prefix != "" {
		if raw, set = lookup(tag); set {
			key = tag
		}
	}
	if !set {
		raw = meta.Tag.Get("default")
	}
	if raw == "" || (set && raw == Empty && textual(field.Type())) {
		if meta.Tag.Get("required") == "true" {
			return fmt.Errorf("%s is required", key)
		}
		if set {
			field.SetZero()
		}
		return nil
	}
	if err := setValue(field, raw); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

// lookup считает пустую переменную незаданной: Helm `value: ""`, compose без подстановки и
// `KEY=` в .env не должны молча обнулять дефолт.
func lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	return v, ok && v != ""
}

// textual — тип, у которого пустое значение отличается от незаданного: строка, список и тип
// со своим разбором.
func textual(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String, reflect.Slice:
		return true
	}
	return decodes(t)
}

func decodes(t reflect.Type) bool {
	pt := reflect.PointerTo(t)
	return pt.Implements(decoderType) || pt.Implements(textUnmarshalerType)
}

func setValue(v reflect.Value, raw string) error {
	if v.Kind() == reflect.Pointer {
		p := reflect.New(v.Type().Elem())
		if err := setValue(p.Elem(), raw); err != nil {
			return err
		}
		v.Set(p)
		return nil
	}
	switch u := v.Addr().Interface().(type) {
	case Decoder:
		return u.Decode(raw)
	case encoding.TextUnmarshaler:
		return u.UnmarshalText([]byte(raw))
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.Type() == durationType {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return err
			}
			v.SetInt(int64(d))
			return nil
		}
		n, err := strconv.ParseInt(raw, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetFloat(f)
	case reflect.Slice:
		items := splitList(raw)
		if len(items) == 0 {
			return errEmptyList
		}
		s := reflect.MakeSlice(v.Type(), len(items), len(items))
		for i, item := range items {
			if err := setValue(s.Index(i), item); err != nil {
				return err
			}
		}
		v.Set(s)
	default:
		return fmt.Errorf("unsupported type %s", v.Type())
	}
	return nil
}

// errEmptyList — список из одних разделителей: пустой список задаёт только Empty, иначе `,`
// молча снимал бы, например, проверку aud.
var errEmptyList = errors.New(`list has no elements: an empty list is "-"`)

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
