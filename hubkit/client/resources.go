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

// --- Tags (tag-backend) ---

// UpsertTag — write-through создания тега в глобальный словарь: {name} →
// каноническая запись {id, name, norm_name}. Дедуп по norm_name на стороне хаба:
// существующий тег вернётся как есть (name первого автора не перетирается).
func (c *Client) UpsertTag(ctx context.Context, name string) (Tag, error) {
	var out dataEnvelope[Tag]
	err := c.do(ctx, http.MethodPost, c.tagBase, "/api/tags", TagInput{Name: name}, &out)
	return out.Data, err
}

// --- Entity tags (company-backend, глобальная принадлежность тегам) ---

// GetEntityTags — текущий набор канонических имён тегов hub-сущности
// (kind ∈ {company,project}). Ходит на company base (владелец принадлежности).
func (c *Client) GetEntityTags(ctx context.Context, kind, id string) ([]string, error) {
	var out dataEnvelope[[]string]
	path := "/api/tags/" + url.PathEscape(kind) + "/" + url.PathEscape(id)
	err := c.do(ctx, http.MethodGet, c.companyBase, path, nil, &out)
	return out.Data, err
}

// ReplaceEntityTags — write-through замена набора тегов hub-сущности
// (PUT, replace-семантика): {tags:[names]} → канонические имена набора. Продукт
// сразу применяет к своей локальной проекции entity-tags (не ждёт pull-синка).
// Пустой набор снимает все теги.
func (c *Client) ReplaceEntityTags(ctx context.Context, kind, id string, names []string) ([]string, error) {
	body := struct {
		Tags []string `json:"tags"`
	}{Tags: names}
	if body.Tags == nil {
		body.Tags = []string{}
	}
	path := "/api/tags/" + url.PathEscape(kind) + "/" + url.PathEscape(id)
	var out dataEnvelope[[]string]
	err := c.do(ctx, http.MethodPut, c.companyBase, path, body, &out)
	return out.Data, err
}

// --- Departments (employee-backend) ---

// GetEmployeeDepartments — текущий набор канонических имён департаментов
// сотрудника. Ходит на employee base (id.stit.tech).
func (c *Client) GetEmployeeDepartments(ctx context.Context, employeeID string) ([]string, error) {
	var out dataEnvelope[[]string]
	path := "/api/employees/" + url.PathEscape(employeeID) + "/departments"
	err := c.do(ctx, http.MethodGet, c.employeeBase, path, nil, &out)
	return out.Data, err
}

// ReplaceEmployeeDepartments — write-through замена набора департаментов
// сотрудника (PUT, replace-семантика): {departments:[names]} → канонические
// имена. Free-form: незнакомое имя заводит департамент в словаре хаба.
func (c *Client) ReplaceEmployeeDepartments(ctx context.Context, employeeID string, names []string) ([]string, error) {
	body := struct {
		Departments []string `json:"departments"`
	}{Departments: names}
	if body.Departments == nil {
		body.Departments = []string{}
	}
	path := "/api/employees/" + url.PathEscape(employeeID) + "/departments"
	var out dataEnvelope[[]string]
	err := c.do(ctx, http.MethodPut, c.employeeBase, path, body, &out)
	return out.Data, err
}

// UpsertDepartment — write-through создания записи в глобальный словарь
// департаментов (продукты: «+ Новый отдел» с доски task): {name} → каноническая
// запись {id, name, norm_name}. Ходит на employee base (id.stit.tech). Дедуп по
// norm_name на стороне хаба: существующий департамент вернётся как есть (name
// первого автора не перетирается, updated_seq словаря не двигается — идемпотентно).
func (c *Client) UpsertDepartment(ctx context.Context, name string) (Department, error) {
	var out dataEnvelope[Department]
	err := c.do(ctx, http.MethodPost, c.employeeBase, "/api/departments", DepartmentInput{Name: name}, &out)
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
