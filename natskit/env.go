package natskit

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// loadEnv заполняет структуру по тегам `envconfig`, читая переменные под prefix
// (пустой prefix — голые имена). Ключ без префикса НЕ подхватывается как
// fallback: иначе именованный инстанс молча унаследовал бы URL и список стримов
// дефолтного и ушёл публиковать в чужую шину.
func loadEnv(prefix string, spec any) error {
	rv := reflect.ValueOf(spec)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("natskit: loadEnv wants non-nil pointer to struct, got %T", spec)
	}
	return loadFields(prefix, rv.Elem())
}

func loadFields(prefix string, sv reflect.Value) error {
	st := sv.Type()
	for i := range st.NumField() {
		field, meta := sv.Field(i), st.Field(i)
		if !meta.IsExported() {
			continue
		}
		if field.Kind() == reflect.Struct {
			if err := loadFields(prefix, field); err != nil {
				return err
			}
			continue
		}

		key := meta.Tag.Get("envconfig")
		if key == "" {
			continue
		}
		if prefix != "" {
			key = prefix + "_" + key
		}
		key = strings.ToUpper(key)

		raw, ok := os.LookupEnv(key)
		if !ok {
			raw = meta.Tag.Get("default")
		}
		if raw == "" {
			continue
		}
		if err := setField(field, raw); err != nil {
			return fmt.Errorf("natskit: %s: %w", key, err)
		}
	}
	return nil
}

func setField(field reflect.Value, raw string) error {
	switch field.Interface().(type) {
	case time.Duration:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		field.Set(reflect.ValueOf(d))
		return nil
	case []string:
		field.Set(reflect.ValueOf(splitList(raw)))
		return nil
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		field.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		field.SetInt(n)
	default:
		return fmt.Errorf("unsupported kind %s", field.Kind())
	}
	return nil
}

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
