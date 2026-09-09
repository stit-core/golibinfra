package tzkit_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/tzkit"
)

func registry() huma.Registry {
	return huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
}

func TestDaySchema(t *testing.T) {
	t.Parallel()

	s := huma.SchemaFromType(registry(), reflect.TypeFor[tzkit.Day]())
	if s.Type != huma.TypeString || s.Format != "date" {
		t.Fatalf("schema type/format = %q/%q, want string/date", s.Type, s.Format)
	}
	if s.Pattern == "" {
		t.Error("schema must carry the YYYY-MM-DD pattern")
	}
	// Нулевой Day уезжает как null — схема обязана это описывать, иначе
	// сгенерированный клиент падает на null, а вход null режет валидатор.
	if !s.Nullable {
		t.Error("Day schema must be nullable: the zero Day marshals to null")
	}
}

func TestWallTimeSchema(t *testing.T) {
	t.Parallel()

	s := huma.SchemaFromType(registry(), reflect.TypeFor[tzkit.WallTime]())
	if s.Type != huma.TypeString || s.Pattern == "" {
		t.Fatalf("schema = %q, pattern %q; want string with an HH:MM pattern", s.Type, s.Pattern)
	}
	if s.Nullable {
		t.Error("WallTime schema must not be nullable: the zero value is midnight, not «unset»")
	}
}

// Валидация huma по схеме кита: день — только строка YYYY-MM-DD либо null,
// время суток — только HH:MM.
func TestSchemaValidation(t *testing.T) {
	t.Parallel()

	reg := registry()
	day := huma.SchemaFromType(reg, reflect.TypeFor[tzkit.Day]())
	wall := huma.SchemaFromType(reg, reflect.TypeFor[tzkit.WallTime]())

	tests := []struct {
		name    string
		schema  *huma.Schema
		value   string
		wantErr bool
	}{
		{name: "day", schema: day, value: `"2026-08-08"`},
		{name: "day null", schema: day, value: `null`},
		{name: "day loose", schema: day, value: `"2026-8-8"`, wantErr: true},
		{name: "day date-time", schema: day, value: `"2026-08-08T00:00:00Z"`, wantErr: true},
		{name: "wall", schema: wall, value: `"17:00"`},
		{name: "wall seconds", schema: wall, value: `"17:00:00"`, wantErr: true},
		{name: "wall null", schema: wall, value: `null`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(tt.value), &v); err != nil {
				t.Fatalf("json: %v", err)
			}
			res := &huma.ValidateResult{}
			huma.Validate(reg, tt.schema, huma.NewPathBuffer([]byte(""), 0), huma.ModeWriteToServer, v, res)
			if got := len(res.Errors) > 0; got != tt.wantErr {
				t.Fatalf("validate(%s) errors = %v, want error: %v", tt.value, res.Errors, tt.wantErr)
			}
		})
	}
}

// Необязательный день в DTO: указатель с omitempty поле опускает, значение с
// omitzero — тоже; голый Day уезжает как null.
func TestOptionalDayInDTO(t *testing.T) {
	t.Parallel()

	type dto struct {
		Ptr      *tzkit.Day `json:"ptr,omitempty"`
		OmitZero tzkit.Day  `json:"omitzero,omitzero"`
		Plain    tzkit.Day  `json:"plain"`
	}
	out, err := json.Marshal(dto{})
	if err != nil || string(out) != `{"plain":null}` {
		t.Fatalf("Marshal = %s, %v", out, err)
	}
}
