package amocrmkit

import (
	"reflect"
	"testing"
)

func TestCodeField(t *testing.T) {
	got := CodeField("PHONE", "+79991234567")
	want := CustomFieldValue{
		FieldCode: "PHONE",
		Values:    []CustomFieldValueItem{{Value: "+79991234567"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CodeField = %+v, want %+v", got, want)
	}
}

func TestCustomFieldValues(t *testing.T) {
	t.Run("empty → nil", func(t *testing.T) {
		if got := CustomFieldValues(nil); got != nil {
			t.Fatalf("CustomFieldValues(nil) = %v, want nil", got)
		}
	})
	t.Run("edits → one value per field", func(t *testing.T) {
		got := CustomFieldValues([]CustomFieldEdit{
			{FieldID: 11, Value: "a"},
			{FieldID: 22, Value: 7},
		})
		want := []CustomFieldValue{
			{FieldID: 11, Values: []CustomFieldValueItem{{Value: "a"}}},
			{FieldID: 22, Values: []CustomFieldValueItem{{Value: 7}}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("CustomFieldValues = %+v, want %+v", got, want)
		}
	})
}
