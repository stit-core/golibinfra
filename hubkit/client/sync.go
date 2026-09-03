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
	ResourceTags       = "tags"
	// ResourceEntityTags — глобальная принадлежность тегам hub-сущностей
	// (company/project); company-backend, snapshot-per-entity replace-семантика.
	// Элементы фида → EntityTagsSnapshot.
	ResourceEntityTags = "entity-tags"
	// ResourceDepartments — глобальный справочник департаментов; employee-backend.
	// Элементы фида → Department.
	ResourceDepartments = "departments"
)

// SyncPage — страница change-feed'а: сырые сущности (в порядке возрастания
// updated_seq) + курсор следующей страницы. Формат ответа хаба:
// `{ "data": [...], "next_after": <seq> }`.
type SyncPage struct {
	Data      []json.RawMessage `json:"data"`
	NextAfter int64             `json:"next_after"`
}

// SyncFeed тянет одну страницу фида ресурса начиная после курсора after. Базы
// роутятся по ресурсу: companies/projects/visibility/entity-tags — company-backend;
// employees/departments — employee-backend; tags — tag-backend.
func (c *Client) SyncFeed(ctx context.Context, resource string, after int64, limit int) (SyncPage, error) {
	base := c.companyBase
	switch resource {
	case ResourceEmployees, ResourceDepartments:
		base = c.employeeBase
	case ResourceTags:
		base = c.tagBase
	}
	path := "/api/sync/" + resource + "?after=" + strconv.FormatInt(after, 10)
	if limit > 0 {
		path += "&limit=" + strconv.Itoa(limit)
	}
	var page SyncPage
	err := c.do(ctx, http.MethodGet, base, path, nil, &page)
	return page, err
}
