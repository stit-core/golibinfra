package flagkit

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNewEnvStore_Parse(t *testing.T) {
	tests := []struct {
		name        string
		definitions string
		wantStrings map[string]string
		wantNames   []string
	}{
		{
			name:        "empty definitions give empty store",
			definitions: "",
			wantStrings: map[string]string{"anything": ""},
			wantNames:   nil,
		},
		{
			name:        "spaces around entry, name and value are trimmed",
			definitions: "  foo = true ,\tbar =  baz  ",
			wantStrings: map[string]string{"foo": "true", "bar": "baz"},
			wantNames:   []string{"bar", "foo"},
		},
		{
			name:        "empty entries are skipped",
			definitions: ",foo=true,, ,bar=baz,",
			wantStrings: map[string]string{"foo": "true", "bar": "baz"},
			wantNames:   []string{"bar", "foo"},
		},
		{
			name:        "duplicate name: last wins",
			definitions: "foo=true,foo=false",
			wantStrings: map[string]string{"foo": "false"},
			wantNames:   []string{"foo"},
		},
		{
			name:        "first = separates, the rest belongs to the value",
			definitions: "dsn=k=v=w",
			wantStrings: map[string]string{"dsn": "k=v=w"},
			wantNames:   []string{"dsn"},
		},
		{
			name:        "empty value is allowed",
			definitions: "foo=",
			wantStrings: map[string]string{"foo": ""},
			wantNames:   []string{"foo"},
		},
		{
			name:        "names are case-sensitive",
			definitions: "FOO=true",
			wantStrings: map[string]string{"FOO": "true", "foo": ""},
			wantNames:   []string{"FOO"},
		},
		{
			name:        "quotes are part of the value",
			definitions: `foo="true"`,
			wantStrings: map[string]string{"foo": `"true"`},
			wantNames:   []string{"foo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewEnvStore(Config{Definitions: tt.definitions})
			if err != nil {
				t.Fatalf("NewEnvStore: %v", err)
			}
			for name, want := range tt.wantStrings {
				if got := store.String(name); got != want {
					t.Errorf("String(%q) = %q, want %q", name, got, want)
				}
			}
			if got := store.Names(); !reflect.DeepEqual(got, tt.wantNames) {
				t.Errorf("Names() = %v, want %v", got, tt.wantNames)
			}
		})
	}
}

func TestNewEnvStore_RejectsInvalidEntries(t *testing.T) {
	tests := []struct {
		name        string
		definitions string
		wantIn      string
	}{
		{"entry without =", "new_checkout", `"new_checkout"`},
		{"semicolon separator becomes part of the value, not an error", "foo=true;bar", ""},
		{"empty name", "=d", `"=d"`},
		{"empty name after trim", " =d", `"=d"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewEnvStore(Config{Definitions: tt.definitions})
			if tt.wantIn == "" {
				if err != nil {
					t.Fatalf("NewEnvStore(%q) = %v, want no error", tt.definitions, err)
				}
				return
			}
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
			if store != nil {
				t.Errorf("store = %v, want nil on error", store)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("err = %v, want the offending entry %s in the message", err, tt.wantIn)
			}
		})
	}
}

func TestValidate_JoinsAllInvalidEntries(t *testing.T) {
	err := Config{Definitions: "ok=1,bad,=d"}.Validate()
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
	for _, want := range []string{`"bad"`, `"=d"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want entry %s reported", err, want)
		}
	}
}

func TestValidate_AcceptsValidAndEmpty(t *testing.T) {
	for _, definitions := range []string{"", "foo=true", " foo = true , bar = baz ", "foo=,"} {
		if err := (Config{Definitions: definitions}).Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", definitions, err)
		}
	}
}

func TestEnvStore_Bool(t *testing.T) {
	// Истинность — по списку strconv.ParseBool; всё прочее (включая "yes"/"on") — false.
	definitions := "one=1,t=t,upperT=T,upper=TRUE,lower=true,title=True," +
		"zero=0,f=f,upperF=FALSE,mixed=TrUe,yes=yes,on=on,empty=,text=baz"

	store, err := NewEnvStore(Config{Definitions: definitions})
	if err != nil {
		t.Fatalf("NewEnvStore: %v", err)
	}

	tests := []struct {
		name string
		want bool
	}{
		{"one", true},
		{"t", true},
		{"upperT", true},
		{"upper", true},
		{"lower", true},
		{"title", true},
		{"zero", false},
		{"f", false},
		{"upperF", false},
		{"mixed", false},
		{"yes", false},
		{"on", false},
		{"empty", false},
		{"text", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		if got := store.Bool(tt.name); got != tt.want {
			t.Errorf("Bool(%q) = %v, want %v (value %q)", tt.name, got, tt.want, store.String(tt.name))
		}
	}
}

func TestEnvStore_ZeroValueIsUsable(t *testing.T) {
	var store EnvStore

	if store.Bool("anything") {
		t.Error("Bool on zero-value store = true, want false")
	}
	if got := store.String("anything"); got != "" {
		t.Errorf("String on zero-value store = %q, want \"\"", got)
	}
	if got := store.Names(); len(got) != 0 {
		t.Errorf("Names on zero-value store = %v, want empty", got)
	}
}

func TestEnvStore_ImplementsStore(t *testing.T) {
	store, err := NewEnvStore(Config{Definitions: "foo=true"})
	if err != nil {
		t.Fatalf("NewEnvStore: %v", err)
	}
	var iface Store = store
	if !iface.Bool("foo") || iface.String("foo") != "true" {
		t.Fatalf("Store = %v/%q, want true/\"true\"", iface.Bool("foo"), iface.String("foo"))
	}
}
