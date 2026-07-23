package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
)

// Канонические имена ресурсов sync-фида (пути /api/sync/<resource>).
const (
	ResourceCompanies  = "companies"
	ResourceProjects   = "projects"
	ResourceEmployees  = "employees"
	ResourceVisibility = "visibility"
)

// SyncPage — страница change-feed'а: сырые сущности (в порядке возрастания
// updated_seq) + курсор следующей страницы. Формат ответа хаба:
// `{ "data": [...], "next_after": <seq> }`.
type SyncPage struct {
	Data      []json.RawMessage `json:"data"`
	NextAfter int64             `json:"next_after"`
}

// SyncFeed тянет одну страницу фида ресурса начиная после курсора after.
// companies/projects/visibility — company-backend; employees — employee-backend.
func (c *Client) SyncFeed(ctx context.Context, resource string, after int64, limit int) (SyncPage, error) {
	base := c.companyBase
	if resource == ResourceEmployees {
		base = c.employeeBase
	}
	path := "/api/sync/" + resource + "?after=" + strconv.FormatInt(after, 10)
	if limit > 0 {
		path += "&limit=" + strconv.Itoa(limit)
	}
	var page SyncPage
	err := c.do(ctx, http.MethodGet, base, path, nil, &page)
	return page, err
}
