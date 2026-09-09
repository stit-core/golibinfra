package amocrmkit

// CustomFieldValue — элемент custom_fields_values[]. Значения полиморфны (амо
// возвращает строки/числа/булевы/объекты в зависимости от типа поля), поэтому
// Value — any; потребитель маппит по field_type/field_id.
type CustomFieldValue struct {
	FieldID   int64                  `json:"field_id,omitempty"`
	FieldName string                 `json:"field_name,omitempty"`
	FieldCode string                 `json:"field_code,omitempty"`
	FieldType string                 `json:"field_type,omitempty"`
	Values    []CustomFieldValueItem `json:"values"`
}

// CustomFieldValueItem — одно значение доп. поля.
type CustomFieldValueItem struct {
	Value    any    `json:"value,omitempty"`
	EnumID   int64  `json:"enum_id,omitempty"`
	EnumCode string `json:"enum_code,omitempty"`
}

// CustomFieldEdit — компактная команда «задать доп-поле по id»: только field_id и
// одно значение. Для outbound-мутаций, где не нужны field_code/enum/несколько
// значений; разворачивается в CustomFieldValue через CustomFieldValues.
type CustomFieldEdit struct {
	FieldID int64
	Value   any
}

// CodeField строит доп-поле, адресованное по field_code (например PHONE/EMAIL):
// потребитель шлёт значение строкой, не зная числового field_id аккаунта.
func CodeField(code, value string) CustomFieldValue {
	return CustomFieldValue{
		FieldCode: code,
		Values:    []CustomFieldValueItem{{Value: value}},
	}
}

// CustomFieldValues разворачивает CustomFieldEdit'ы в custom_fields_values[]
// (одно значение на поле). Пустой вход → nil.
func CustomFieldValues(edits []CustomFieldEdit) []CustomFieldValue {
	if len(edits) == 0 {
		return nil
	}
	out := make([]CustomFieldValue, 0, len(edits))
	for _, e := range edits {
		out = append(out, CustomFieldValue{
			FieldID: e.FieldID,
			Values:  []CustomFieldValueItem{{Value: e.Value}},
		})
	}
	return out
}

// Tag — тег сущности (в GET-ответе _embedded.tags).
type Tag struct {
	ID    int64  `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
}

// TagRef — ссылка на тег для tags_to_add/tags_to_delete (по id или name).
type TagRef struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// CreatedEntity — типовой элемент ответа POST/PATCH batch
// (_embedded.<entity>[]{id, request_id, updated_at, _links}).
type CreatedEntity struct {
	ID        int64  `json:"id"`
	RequestID string `json:"request_id,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	Links     Links  `json:"_links"`
}

// ContactRef — элемент _embedded.contacts сделки.
type ContactRef struct {
	ID     int64 `json:"id"`
	IsMain bool  `json:"is_main,omitempty"`
}

// CompanyRef — элемент _embedded.companies сделки.
type CompanyRef struct {
	ID int64 `json:"id"`
}
