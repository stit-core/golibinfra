package tzkit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
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

// Неверный день или время суток в запросе — ответ по-русски: описание шаблона huma пишет в сообщение поля
// («значение должно соответствовать: <описание>»), и человек видит, как записать значение.
func TestSchemaFieldErrorsAreRussian(t *testing.T) {
	t.Parallel()

	type slot struct {
		Body struct {
			Day  tzkit.Day      `json:"day"`
			Wall tzkit.WallTime `json:"wall"`
		}
	}
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Put(api, "/slot", func(context.Context, *slot) (*struct{}, error) { return nil, nil }) // тело ответа не нужно
	})
	const (
		dayPattern  = "значение должно соответствовать: дата вида 2026-11-09 – год, месяц и день"
		dayFormat   = "нужна дата, например 2026-11-09"
		wallPattern = "значение должно соответствовать: время вида 09:30 – часы и минуты"
	)
	tests := []struct {
		name string
		body map[string]any
		want map[string][]string
	}{
		{
			name: "another notation",
			body: map[string]any{"day": "09.11.2026", "wall": "9:30"},
			want: map[string][]string{"body.day": {dayPattern, dayFormat}, "body.wall": {wallPattern}},
		},
		{
			name: "no such day and time",
			body: map[string]any{"day": "2026-13-40", "wall": "25:00"},
			want: map[string][]string{"body.day": {dayFormat}, "body.wall": {wallPattern}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := api.Put("/slot", tt.body)
			handlertest.AssertAPIError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_FAILED")
			got := map[string][]string{}
			for _, e := range handlertest.DecodeAPIError(t, resp).Errors {
				got[e.Location] = append(got[e.Location], e.Message)
			}
			for location, want := range tt.want {
				if !slices.Equal(got[location], want) {
					t.Errorf("%s: messages = %q, want %q", location, got[location], want)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("errors at %v, want only at %v", got, tt.want)
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
