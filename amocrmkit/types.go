package amocrmkit

// Shared wire-типы REST v4, общие для большинства сущностей.

// CustomFieldValue — элемент custom_fields_values[]. Значения полиморфны (амо
// возвращает строки/числа/булевы/объекты в зависимости от типа поля), поэтому
// Value — any; gateway маппит по field_type/field_id.
type CustomFieldValue struct {
	FieldID   int                    `json:"field_id,omitempty"`
	FieldName string                 `json:"field_name,omitempty"`
	FieldCode string                 `json:"field_code,omitempty"`
	FieldType string                 `json:"field_type,omitempty"`
	Values    []CustomFieldValueItem `json:"values"`
}

// CustomFieldValueItem — одно значение доп. поля.
type CustomFieldValueItem struct {
	Value    any    `json:"value,omitempty"`
	EnumID   int    `json:"enum_id,omitempty"`
	EnumCode string `json:"enum_code,omitempty"`
}

// Tag — тег сущности (в GET-ответе _embedded.tags).
type Tag struct {
	ID    int    `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
}

// TagRef — ссылка на тег для tags_to_add/tags_to_delete (по id или name).
type TagRef struct {
	ID   int    `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// CreatedEntity — типовой элемент ответа POST/PATCH batch
// (_embedded.<entity>[]{id, request_id, updated_at, _links}).
type CreatedEntity struct {
	ID        int    `json:"id"`
	RequestID string `json:"request_id,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	Links     Links  `json:"_links,omitempty"`
}

// ContactRef / CompanyRef — элементы _embedded.contacts/companies сделки.
type ContactRef struct {
	ID     int  `json:"id"`
	IsMain bool `json:"is_main,omitempty"`
}

type CompanyRef struct {
	ID int `json:"id"`
}
