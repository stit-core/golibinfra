package httpkit_test

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
)

// VGK-06: таблица ответов кита в docs/testing.md — то, что видит в OpenAPI тест на API из handlertest.NewAPI с конфигом
// по умолчанию: строка — вид операции, ячейка — есть ли у неё ответ статуса столбца. Дока — источник: строка без
// операции в тесте и операция без строки — отказ.
func TestTestingDoc_KitResponsesTable(t *testing.T) {
	raw, err := os.ReadFile("docs/testing.md")
	if err != nil {
		t.Fatal(err)
	}
	const header = "| Операция | 401 | 413 | 429 | 503 |"
	_, table, found := strings.Cut(string(raw), header+"\n|---|---|---|---|---|\n")
	if !found {
		t.Fatalf("docs/testing.md: the table of kit responses %q is not found", header)
	}
	statuses := []int{http.StatusUnauthorized, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusServiceUnavailable}
	rows := map[string][]bool{}
	for line := range strings.Lines(table) {
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 1+len(statuses) {
			t.Fatalf("docs/testing.md: row %q has %d cells, want %d", line, len(cells), 1+len(statuses))
		}
		var has []bool
		for _, c := range cells[1:] {
			switch strings.TrimSpace(c) {
			case "да":
				has = append(has, true)
			case "—":
				has = append(has, false)
			default:
				t.Fatalf("docs/testing.md: row %q: cell %q is neither «да» nor «—»", line, c)
			}
		}
		rows[strings.TrimSpace(cells[0])] = has
	}

	type form struct {
		RawBody huma.MultipartFormFiles[struct {
			File huma.FormFile `form:"file"`
		}]
	}
	noBody := func(context.Context, *struct{}) (*struct{}, error) { return nil, nil } // тело ответа не нужно
	secured := []map[string][]string{{"Bearer": {}}}
	kinds := map[string]string{ // строка таблицы → операция
		"с телом (`Body`, `RawBody`)": "body",
		"multipart-форма":             "form",
		"поток SSE (`sse.Register`)":  "events",
		"под `Security`":              "me",
		"необязательная аутентификация (`{}` в `Security`)": "maybe",
		"маршрут мимо huma, описанный в OpenAPI руками":     "rawRoute",
	}
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "body", Method: http.MethodPost, Path: "/body"}),
			func(context.Context, *struct{ RawBody []byte }) (*struct{}, error) { return nil, nil })
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "form", Method: http.MethodPost, Path: "/form"}),
			func(context.Context, *form) (*struct{}, error) { return nil, nil })
		sse.Register(api, httpkit.Public(huma.Operation{OperationID: "events", Method: http.MethodGet, Path: "/events"}),
			map[string]any{"tick": struct{}{}}, func(context.Context, *struct{}, sse.Sender) {})
		huma.Register(api, huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/me", Security: secured}, noBody)
		huma.Register(api, huma.Operation{
			OperationID: "maybe", Method: http.MethodGet, Path: "/maybe", Security: []map[string][]string{{"Bearer": {}}, {}},
		}, noBody)
		api.OpenAPI().AddOperation(&huma.Operation{
			OperationID: "rawRoute", Method: http.MethodPost, Path: "/raw-route", Security: secured,
			RequestBody: &huma.RequestBody{Content: map[string]*huma.MediaType{"application/json": {}}},
		})
	}, handlertest.WithSecuritySchemes(httpkit.SecuritySchemeEntry{
		Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"},
		Extract: func(r *http.Request) (context.Context, error) { return r.Context(), nil },
	}))
	ops := operations(api.OpenAPI())
	for row, id := range kinds {
		has, ok := rows[row]
		if !ok {
			t.Errorf("docs/testing.md: no row %q in the table of kit responses", row)
			continue
		}
		delete(rows, row)
		for i, status := range statuses {
			if got := ops[id].Responses[strconv.Itoa(status)] != nil; got != has[i] {
				t.Errorf("docs/testing.md: %q — %d is %v in the table, %v in OpenAPI of %s", row, status, has[i], got, id)
			}
		}
	}
	for row := range rows {
		t.Errorf("docs/testing.md: row %q has no operation in the test", row)
	}
}
