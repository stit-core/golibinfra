package amocrmkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// restMux маршрутизирует тестовые ответы по "METHOD path".
func restMux(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bearer обязателен на REST-вызовах.
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		key := r.Method + " " + r.URL.Path
		if h, ok := routes[key]; ok {
			h(w, r)
			return
		}
		t.Errorf("unexpected route %s", key)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListLeads_DecodesEmbeddedAndPagination(t *testing.T) {
	srv := restMux(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/leads": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("limit") != "2" {
				t.Errorf("limit query = %q, want 2", r.URL.Query().Get("limit"))
			}
			_, _ = w.Write([]byte(`{
				"_page": 2,
				"_links": {"next": {"href": "https://x/api/v4/leads?page=3"}},
				"_embedded": {"leads": [
					{"id": 19619, "name": "Сделка", "price": 46333, "status_id": 142, "pipeline_id": 1300, "is_deleted": false}
				]}
			}`))
		},
	})
	c := newTestClient(t, srv.URL)

	page, err := c.ListLeads(context.Background(), testAccount(), ListParams{Limit: 2})
	if err != nil {
		t.Fatalf("ListLeads: %v", err)
	}
	if page.Page != 2 || page.Links.Next == nil {
		t.Errorf("pagination not decoded: %+v", page)
	}
	if len(page.Items) != 1 || page.Items[0].ID != 19619 || page.Items[0].Price != 46333 {
		t.Fatalf("items = %+v", page.Items)
	}
}

func TestCreateLeads_SendsArrayBodyAndDecodesCreated(t *testing.T) {
	var gotBody []LeadInput
	srv := restMux(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v4/leads": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"_embedded":{"leads":[{"id":10185151,"request_id":"0"}]}}`))
		},
	})
	c := newTestClient(t, srv.URL)

	out, err := c.CreateLeads(context.Background(), testAccount(), []LeadInput{{Name: "X", TagsToAdd: []TagRef{{Name: "t"}}}})
	if err != nil {
		t.Fatalf("CreateLeads: %v", err)
	}
	if len(gotBody) != 1 || gotBody[0].Name != "X" || gotBody[0].TagsToAdd[0].Name != "t" {
		t.Errorf("request body = %+v", gotBody)
	}
	if len(out) != 1 || out[0].ID != 10185151 || out[0].RequestID != "0" {
		t.Errorf("created = %+v", out)
	}
}

func TestDelete_NoContent(t *testing.T) {
	srv := restMux(t, map[string]func(http.ResponseWriter, *http.Request){
		"DELETE /api/v4/customers/segments/55": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})
	c := newTestClient(t, srv.URL)
	if err := c.DeleteSegment(context.Background(), testAccount(), 55); err != nil {
		t.Fatalf("DeleteSegment: %v", err)
	}
}

func TestList_204IsEmptyNotError(t *testing.T) {
	srv := restMux(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/contacts": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent) // RU-квирк: 204 на GET = «нет данных»
		},
	})
	c := newTestClient(t, srv.URL)
	page, err := c.ListContacts(context.Background(), testAccount(), ListParams{})
	if err != nil {
		t.Fatalf("204 must not be an error: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("expected empty page, got %+v", page.Items)
	}
}

func TestError_404Classified(t *testing.T) {
	srv := restMux(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/tasks": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"title":"Not Found","status":404,"detail":"nope"}`))
		},
	})
	c := newTestClient(t, srv.URL)
	_, err := c.ListTasks(context.Background(), testAccount(), ListParams{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Detail != "nope" {
		t.Errorf("APIError not attached: %v", err)
	}
}

func TestError_429ParsesRetryAfterFromBody(t *testing.T) {
	srv := restMux(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/companies": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			// retry_after — поле тела (не заголовок), per error-model.md
			_, _ = w.Write([]byte(`{"title":"Too Many Requests","status":429,"retry_after":300}`))
		},
	})
	c := newTestClient(t, srv.URL)
	_, err := c.ListCompanies(context.Background(), testAccount(), ListParams{})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 300 {
		t.Errorf("RetryAfter = %v, want 300 (from body)", apiErr)
	}
}

func TestGetAmojoID_ExtractsField(t *testing.T) {
	srv := restMux(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/account": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("with") != "amojo_id" {
				t.Errorf("with = %q, want amojo_id", r.URL.Query().Get("with"))
			}
			_, _ = w.Write([]byte(`{"id":11223344,"name":"acc","amojo_id":"52a350c1-uuid"}`))
		},
	})
	c := newTestClient(t, srv.URL)
	id, err := c.GetAmojoID(context.Background(), testAccount())
	if err != nil || id != "52a350c1-uuid" {
		t.Fatalf("GetAmojoID = %q, %v", id, err)
	}
}
