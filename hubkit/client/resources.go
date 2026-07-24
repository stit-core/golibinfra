package client

import (
	"context"
	"net/http"
	"net/url"
)

// --- Companies (company-backend) ---

func (c *Client) ListCompanies(ctx context.Context, p ListParams) ([]Company, error) {
	var out dataEnvelope[[]Company]
	err := c.do(ctx, http.MethodGet, c.companyBase, "/api/companies"+listQuery(p), nil, &out)
	return out.Data, err
}

func (c *Client) GetCompany(ctx context.Context, id string) (Company, error) {
	var out dataEnvelope[Company]
	err := c.do(ctx, http.MethodGet, c.companyBase, "/api/companies/"+url.PathEscape(id), nil, &out)
	return out.Data, err
}

func (c *Client) GetCompanyBySlug(ctx context.Context, slug string) (Company, error) {
	var out dataEnvelope[Company]
	err := c.do(ctx, http.MethodGet, c.companyBase, "/api/companies/by-slug?slug="+url.QueryEscape(slug), nil, &out)
	return out.Data, err
}

func (c *Client) CreateCompany(ctx context.Context, in CompanyInput) (Company, error) {
	var out dataEnvelope[Company]
	err := c.do(ctx, http.MethodPost, c.companyBase, "/api/companies", in, &out)
	return out.Data, err
}

func (c *Client) UpdateCompany(ctx context.Context, id string, in CompanyInput) (Company, error) {
	var out dataEnvelope[Company]
	err := c.do(ctx, http.MethodPatch, c.companyBase, "/api/companies/"+url.PathEscape(id), in, &out)
	return out.Data, err
}

func (c *Client) ArchiveCompany(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, c.companyBase, "/api/companies/"+url.PathEscape(id)+"/archive", nil, nil)
}

func (c *Client) UnarchiveCompany(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, c.companyBase, "/api/companies/"+url.PathEscape(id)+"/unarchive", nil, nil)
}

func (c *Client) ListCompanyProjects(ctx context.Context, companyID string, p ListParams) ([]Project, error) {
	var out dataEnvelope[[]Project]
	err := c.do(ctx, http.MethodGet, c.companyBase, "/api/companies/"+url.PathEscape(companyID)+"/projects"+listQuery(p), nil, &out)
	return out.Data, err
}

// --- Projects (company-backend) ---

func (c *Client) ListProjects(ctx context.Context, p ListParams) ([]Project, error) {
	var out dataEnvelope[[]Project]
	err := c.do(ctx, http.MethodGet, c.companyBase, "/api/projects"+listQuery(p), nil, &out)
	return out.Data, err
}

func (c *Client) GetProject(ctx context.Context, id string) (Project, error) {
	var out dataEnvelope[Project]
	err := c.do(ctx, http.MethodGet, c.companyBase, "/api/projects/"+url.PathEscape(id), nil, &out)
	return out.Data, err
}

func (c *Client) CreateProject(ctx context.Context, in ProjectInput) (Project, error) {
	var out dataEnvelope[Project]
	err := c.do(ctx, http.MethodPost, c.companyBase, "/api/projects", in, &out)
	return out.Data, err
}

func (c *Client) UpdateProject(ctx context.Context, id string, in ProjectInput) (Project, error) {
	var out dataEnvelope[Project]
	err := c.do(ctx, http.MethodPatch, c.companyBase, "/api/projects/"+url.PathEscape(id), in, &out)
	return out.Data, err
}

func (c *Client) ArchiveProject(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, c.companyBase, "/api/projects/"+url.PathEscape(id)+"/archive", nil, nil)
}

func (c *Client) UnarchiveProject(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, c.companyBase, "/api/projects/"+url.PathEscape(id)+"/unarchive", nil, nil)
}

// --- Visibility (company-backend) ---

func (c *Client) ListVisibility(ctx context.Context, service string) ([]Visibility, error) {
	var out dataEnvelope[[]Visibility]
	err := c.do(ctx, http.MethodGet, c.companyBase, "/api/visibility?service="+url.QueryEscape(service), nil, &out)
	return out.Data, err
}

// SetVisibility скрывает/показывает сущность для сервиса (PUT idempotent).
func (c *Client) SetVisibility(ctx context.Context, service, kind, id string, hidden bool) error {
	_, err := c.SetVisibilityEntry(ctx, service, kind, id, hidden)
	return err
}

// SetVisibilityEntry — как SetVisibility, но возвращает обновлённую сущность (со
// свежим updated_seq). Нужен потребителю для write-through в локальную проекцию:
// toggle виден в read сразу, не дожидаясь pull-синка.
func (c *Client) SetVisibilityEntry(ctx context.Context, service, kind, id string, hidden bool) (Visibility, error) {
	body := struct {
		Hidden bool `json:"hidden"`
	}{Hidden: hidden}
	path := "/api/visibility/" + url.PathEscape(service) + "/" + url.PathEscape(kind) + "/" + url.PathEscape(id)
	var out dataEnvelope[Visibility]
	err := c.do(ctx, http.MethodPut, c.companyBase, path, body, &out)
	return out.Data, err
}

// --- Employees (employee-backend) ---

func (c *Client) ListEmployees(ctx context.Context, p ListParams) ([]Employee, error) {
	var out dataEnvelope[[]Employee]
	err := c.do(ctx, http.MethodGet, c.employeeBase, "/api/employees"+listQuery(p), nil, &out)
	return out.Data, err
}

func (c *Client) GetEmployee(ctx context.Context, id string) (Employee, error) {
	var out dataEnvelope[Employee]
	err := c.do(ctx, http.MethodGet, c.employeeBase, "/api/employees/"+url.PathEscape(id), nil, &out)
	return out.Data, err
}
