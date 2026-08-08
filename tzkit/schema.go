package tzkit

import "github.com/danielgtaylor/huma/v2"

// Schema — huma.SchemaProvider: в OpenAPI день и время суток описываются как
// строки с явным паттерном, а не как date-time. Иначе клиент, увидев
// "format": "date-time", шлёт момент с офсетом — и день промахивается на сутки.
func (Day) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:               huma.TypeString,
		Format:             "date",
		Pattern:            `^\d{4}-\d{2}-\d{2}$`,
		PatternDescription: "календарный день YYYY-MM-DD",
		Examples:           []any{"2026-08-08"},
	}
}

func (WallTime) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:               huma.TypeString,
		Pattern:            `^([01][0-9]|2[0-3]):[0-5][0-9]$`,
		PatternDescription: "время суток HH:MM",
		Examples:           []any{"17:00"},
	}
}
