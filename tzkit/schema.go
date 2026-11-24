package tzkit

import "github.com/danielgtaylor/huma/v2"

// Schema — huma.SchemaProvider: в OpenAPI день описывается как строка с явным
// паттерном, а не как date-time. Иначе клиент, увидев "format": "date-time",
// шлёт момент с офсетом — и день промахивается на сутки. Nullable — потому что
// нулевой Day сериализуется в null: схема обязана описывать то, что уезжает на
// провод (huma для SchemaProvider свою nullable-логику не применяет).
func (Day) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:               huma.TypeString,
		Format:             "date",
		Nullable:           true,
		Pattern:            `^\d{4}-\d{2}-\d{2}$`,
		PatternDescription: "дата вида 2026-11-09 – год, месяц и день",
		Examples:           []any{"2026-08-08"},
	}
}

// Schema — huma.SchemaProvider: время суток в OpenAPI — строка HH:MM.
func (WallTime) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:               huma.TypeString,
		Pattern:            `^([01][0-9]|2[0-3]):[0-5][0-9]$`,
		PatternDescription: "время вида 09:30 – часы и минуты",
		Examples:           []any{"17:00"},
	}
}
