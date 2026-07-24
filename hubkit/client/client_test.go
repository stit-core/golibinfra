package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
