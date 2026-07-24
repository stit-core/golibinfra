package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/hubkit/client"
)

func newClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return client.New(client.Config{BaseURL: srv.URL, ServiceToken: "tok", ServiceName: "deal"}, srv.Client())
}

func TestGetCompany_OK(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("нет Bearer-заголовка: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/companies/c1" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"id": "c1", "name": "Acme", "slug": "acme", "updated_seq": 7},
		})
	})
	got, err := c.GetCompany(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetCompany: %v", err)
	}
	if got.ID != "c1" || got.Name != "Acme" || got.UpdatedSeq != 7 {
		t.Fatalf("company=%+v", got)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, client.ErrNotFound},
		{http.StatusConflict, client.ErrConflict},
		{http.StatusUnauthorized, client.ErrUnauthorized},
		{http.StatusForbidden, client.ErrUnauthorized},
		{http.StatusInternalServerError, client.ErrUnavailable},
		{http.StatusBadGateway, client.ErrUnavailable},
	}
	for _, tc := range cases {
		c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		})
		_, err := c.GetCompany(context.Background(), "x")
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: ожидали %v, получили %v", tc.status, tc.want, err)
		}
	}
}

func TestTransportError_Unavailable(t *testing.T) {
	// Клиент на несуществующий адрес — транспортная ошибка маппится в Unavailable.
	c := client.New(client.Config{BaseURL: "http://127.0.0.1:0"}, http.DefaultClient)
	if _, err := c.GetCompany(context.Background(), "x"); !errors.Is(err, client.ErrUnavailable) {
		t.Fatalf("ожидали ErrUnavailable, получили %v", err)
	}
}

func TestSyncFeed_RoutesAndParses(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/companies" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.URL.Query().Get("after") != "5" || r.URL.Query().Get("limit") != "100" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       []map[string]any{{"id": "c6", "updated_seq": 6}},
			"next_after": 6,
		})
	})
	page, err := c.SyncFeed(context.Background(), client.ResourceCompanies, 5, 100)
	if err != nil {
		t.Fatalf("SyncFeed: %v", err)
	}
	if page.NextAfter != 6 || len(page.Data) != 1 {
		t.Fatalf("page=%+v", page)
	}
}

func TestUpsertTag_OK(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tags" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("нет Bearer-заголовка: %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Name != "Срочно" {
			t.Errorf("name=%q", body.Name)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"id": "t1", "name": "Срочно", "norm_name": "срочно"},
		})
	})
	got, err := c.UpsertTag(context.Background(), "Срочно")
	if err != nil {
		t.Fatalf("UpsertTag: %v", err)
	}
	if got.ID != "t1" || got.Name != "Срочно" || got.NormName != "срочно" {
		t.Fatalf("tag=%+v", got)
	}
}

func TestUpsertTag_ErrorMapping(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := c.UpsertTag(context.Background(), "x"); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("ожидали ErrUnauthorized, получили %v", err)
	}
}

// Клиент тегов ходит на HUB_TAG_BASE_URL, а не на company BaseURL.
func TestTagRouting_UsesTagBase(t *testing.T) {
	companySrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("запрос ушёл на company base: %s", r.URL.Path)
	}))
	t.Cleanup(companySrv.Close)

	var upsertHit, syncHit bool
	tagSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			upsertHit = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"id": "t1", "name": "a", "norm_name": "a"},
			})
		case "/api/sync/tags":
			syncHit = true
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "next_after": 0})
		default:
			t.Errorf("неожиданный path на tag base: %s", r.URL.Path)
		}
	}))
	t.Cleanup(tagSrv.Close)

	c := client.New(client.Config{
		BaseURL:      companySrv.URL,
		TagBaseURL:   tagSrv.URL,
		ServiceToken: "tok",
	}, tagSrv.Client())

	if _, err := c.UpsertTag(context.Background(), "a"); err != nil {
		t.Fatalf("UpsertTag: %v", err)
	}
	if _, err := c.SyncFeed(context.Background(), client.ResourceTags, 0, 100); err != nil {
		t.Fatalf("SyncFeed tags: %v", err)
	}
	if !upsertHit || !syncHit {
		t.Fatalf("upsertHit=%v syncHit=%v", upsertHit, syncHit)
	}
}

func TestGetEntityTags_OK(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tags/company/c1" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{"Срочно", "VIP"}})
	})
	got, err := c.GetEntityTags(context.Background(), "company", "c1")
	if err != nil {
		t.Fatalf("GetEntityTags: %v", err)
	}
	if len(got) != 2 || got[0] != "Срочно" || got[1] != "VIP" {
		t.Fatalf("tags=%v", got)
	}
}

func TestReplaceEntityTags_OK(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/tags/project/p1" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Tags) != 1 || body.Tags[0] != "Срочно" {
			t.Errorf("tags body=%v", body.Tags)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{"Срочно"}})
	})
	got, err := c.ReplaceEntityTags(context.Background(), "project", "p1", []string{"Срочно"})
	if err != nil {
		t.Fatalf("ReplaceEntityTags: %v", err)
	}
	if len(got) != 1 || got[0] != "Срочно" {
		t.Fatalf("tags=%v", got)
	}
}

// nil набор сериализуется как [] (снятие всех тегов), не как null.
func TestReplaceEntityTags_NilSendsEmptyArray(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"tags":[]`) {
			t.Errorf("ожидали tags:[] в теле, получили %s", raw)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{}})
	})
	if _, err := c.ReplaceEntityTags(context.Background(), "company", "c1", nil); err != nil {
		t.Fatalf("ReplaceEntityTags: %v", err)
	}
}

func TestReplaceEmployeeDepartments_OK(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/employees/e1/departments" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Departments []string `json:"departments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Departments) != 2 || body.Departments[0] != "Разработка" {
			t.Errorf("departments body=%v", body.Departments)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{"Разработка", "QA"}})
	})
	got, err := c.ReplaceEmployeeDepartments(context.Background(), "e1", []string{"Разработка", "QA"})
	if err != nil {
		t.Fatalf("ReplaceEmployeeDepartments: %v", err)
	}
	if len(got) != 2 || got[1] != "QA" {
		t.Fatalf("departments=%v", got)
	}
}

func TestGetEmployeeDepartments_OK(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/employees/e1/departments" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{"Разработка"}})
	})
	got, err := c.GetEmployeeDepartments(context.Background(), "e1")
	if err != nil {
		t.Fatalf("GetEmployeeDepartments: %v", err)
	}
	if len(got) != 1 || got[0] != "Разработка" {
		t.Fatalf("departments=%v", got)
	}
}

// entity-tags/departments ходят на свои базы: entity-tags и его write-through —
// company base; departments и его write-through — employee base.
func TestEntityTagsAndDepartments_BaseRouting(t *testing.T) {
	var companyPaths, employeePaths []string
	companySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		companyPaths = append(companyPaths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/api/sync/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "next_after": 0})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{}})
	}))
	t.Cleanup(companySrv.Close)
	employeeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		employeePaths = append(employeePaths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/api/sync/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "next_after": 0})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{}})
	}))
	t.Cleanup(employeeSrv.Close)

	c := client.New(client.Config{
		BaseURL:         companySrv.URL,
		EmployeeBaseURL: employeeSrv.URL,
		ServiceToken:    "tok",
	}, employeeSrv.Client())

	ctx := context.Background()
	if _, err := c.GetEntityTags(ctx, "company", "c1"); err != nil {
		t.Fatalf("GetEntityTags: %v", err)
	}
	if _, err := c.ReplaceEntityTags(ctx, "company", "c1", []string{"x"}); err != nil {
		t.Fatalf("ReplaceEntityTags: %v", err)
	}
	if _, err := c.SyncFeed(ctx, client.ResourceEntityTags, 0, 100); err != nil {
		t.Fatalf("SyncFeed entity-tags: %v", err)
	}
	if _, err := c.GetEmployeeDepartments(ctx, "e1"); err != nil {
		t.Fatalf("GetEmployeeDepartments: %v", err)
	}
	if _, err := c.ReplaceEmployeeDepartments(ctx, "e1", []string{"y"}); err != nil {
		t.Fatalf("ReplaceEmployeeDepartments: %v", err)
	}
	if _, err := c.SyncFeed(ctx, client.ResourceDepartments, 0, 100); err != nil {
		t.Fatalf("SyncFeed departments: %v", err)
	}

	joinC := strings.Join(companyPaths, ",")
	if !strings.Contains(joinC, "/api/tags/company/c1") || !strings.Contains(joinC, "/api/sync/entity-tags") {
		t.Fatalf("entity-tags не ушли на company base: %v", companyPaths)
	}
	joinE := strings.Join(employeePaths, ",")
	if !strings.Contains(joinE, "/api/employees/e1/departments") || !strings.Contains(joinE, "/api/sync/departments") {
		t.Fatalf("departments не ушли на employee base: %v", employeePaths)
	}
	// перекрёстной утечки нет
	if strings.Contains(joinC, "departments") || strings.Contains(joinE, "/api/tags/") {
		t.Fatalf("перекрёстная утечка баз: company=%v employee=%v", companyPaths, employeePaths)
	}
}

// Фид entity-tags — snapshot-per-entity: элемент несёт tag_ids[] и (kind,id),
// а не id. Продукт unmarshal'ит его в client.EntityTagsSnapshot. Пустой набор — [].
func TestSyncFeed_EntityTags_ParsesSnapshot(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/entity-tags" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"entity_kind": "company", "entity_id": "c1", "tag_ids": []string{"t1", "t2"}, "updated_seq": 4},
				{"entity_kind": "project", "entity_id": "p9", "tag_ids": []string{}, "updated_seq": 5},
			},
			"next_after": 5,
		})
	})
	page, err := c.SyncFeed(context.Background(), client.ResourceEntityTags, 0, 100)
	if err != nil {
		t.Fatalf("SyncFeed entity-tags: %v", err)
	}
	if page.NextAfter != 5 || len(page.Data) != 2 {
		t.Fatalf("page=%+v", page)
	}
	var first client.EntityTagsSnapshot
	if err := json.Unmarshal(page.Data[0], &first); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if first.EntityKind != "company" || first.EntityID != "c1" || len(first.TagIDs) != 2 || first.UpdatedSeq != 4 {
		t.Fatalf("snapshot=%+v", first)
	}
	var second client.EntityTagsSnapshot
	_ = json.Unmarshal(page.Data[1], &second)
	if len(second.TagIDs) != 0 {
		t.Fatalf("пустой набор ожидали [], получили %v", second.TagIDs)
	}
}

// Фид employees несёт department_ids[]. Обратная совместимость: элемент старого
// сервера (без поля) unmarshal'ится в Employee с DepartmentIDs=nil, без ошибки.
func TestEmployeeFeed_DepartmentIDs_BackwardCompat(t *testing.T) {
	withDeps := []byte(`{"id":"e1","subject":"s1","department_ids":["d1","d2"],"updated_seq":3}`)
	legacy := []byte(`{"id":"e2","subject":"s2","updated_seq":4}`)

	var newer client.Employee
	if err := json.Unmarshal(withDeps, &newer); err != nil {
		t.Fatalf("unmarshal newer: %v", err)
	}
	if len(newer.DepartmentIDs) != 2 || newer.DepartmentIDs[0] != "d1" {
		t.Fatalf("department_ids=%v", newer.DepartmentIDs)
	}

	var old client.Employee
	if err := json.Unmarshal(legacy, &old); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if old.DepartmentIDs != nil {
		t.Fatalf("старый фид без поля → nil, получили %v", old.DepartmentIDs)
	}
}

func TestSyncFeed_Departments_Parses(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/departments" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "d1", "name": "Разработка", "norm_name": "разработка", "updated_seq": 2},
			},
			"next_after": 2,
		})
	})
	page, err := c.SyncFeed(context.Background(), client.ResourceDepartments, 0, 100)
	if err != nil {
		t.Fatalf("SyncFeed departments: %v", err)
	}
	var d client.Department
	if err := json.Unmarshal(page.Data[0], &d); err != nil {
		t.Fatalf("unmarshal department: %v", err)
	}
	if d.ID != "d1" || d.Name != "Разработка" || d.NormName != "разработка" || d.UpdatedSeq != 2 {
		t.Fatalf("department=%+v", d)
	}
}

func TestSetVisibility(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/visibility/deal/company/c1" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Hidden bool `json:"hidden"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Hidden {
			t.Error("hidden не передан")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.SetVisibility(context.Background(), "deal", "company", "c1", true); err != nil {
		t.Fatalf("SetVisibility: %v", err)
	}
}

func TestSetVisibilityEntry_ReturnsEntity(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/visibility/deal/company/c1" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"service": "deal", "entity_kind": "company", "entity_id": "c1",
				"hidden": true, "updated_seq": 42,
			},
		})
	})
	got, err := c.SetVisibilityEntry(context.Background(), "deal", "company", "c1", true)
	if err != nil {
		t.Fatalf("SetVisibilityEntry: %v", err)
	}
	if got.EntityID != "c1" || !got.Hidden || got.UpdatedSeq != 42 {
		t.Fatalf("entry = %+v, want c1 hidden seq=42", got)
	}
}
