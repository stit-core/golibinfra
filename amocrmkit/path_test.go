package amocrmkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recordingServer запоминает путь и query последнего запроса.
type recordingServer struct {
	*httptest.Server
	path  string
	query string
	sig   string
	hits  int
}

func newRecordingServer(t *testing.T, body string) *recordingServer {
	t.Helper()
	rec := &recordingServer{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits++
		rec.path = r.URL.EscapedPath()
		rec.query = r.URL.RawQuery
		rec.sig = r.Header.Get("X-Signature")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(rec.Close)
	return rec
}

// Значение из внешней системы не должно переписывать путь и query вызова.
func TestPathSegmentsAreEscaped(t *testing.T) {
	ctx := context.Background()
	acc := testAccount()

	cases := []struct {
		name string
		do   func(c *Client) error
		want string
	}{
		{
			name: "unsorted uid with query",
			do: func(c *Client) error {
				_, err := c.AcceptUnsorted(ctx, acc, "uid?limit=999&page=1", UnsortedAcceptInput{})
				return err
			},
			want: "/api/v4/leads/unsorted/uid%3Flimit=999&page=1/accept",
		},
		{
			name: "unsorted uid with fragment",
			do:   func(c *Client) error { return c.DeclineUnsorted(ctx, acc, "abc#frag") },
			want: "/api/v4/leads/unsorted/abc%23frag/decline",
		},
		{
			name: "file uuid with traversal",
			do: func(c *Client) error {
				_, err := c.GetFileLinks(ctx, acc, "../../oauth2/access_token")
				return err
			},
			want: "/api/v4/files/..%2F..%2Foauth2%2Faccess_token/links",
		},
		{
			name: "custom field group id",
			do: func(c *Client) error {
				return c.DeleteCustomFieldGroup(ctx, acc, "leads", "g/../..")
			},
			want: "/api/v4/leads/custom_fields/groups/g%2F..%2F..",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecordingServer(t, `{}`)
			c := newTestClient(t, rec.URL)
			if err := tc.do(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if rec.path != tc.want {
				t.Errorf("path = %q, want %q", rec.path, tc.want)
			}
		})
	}
}

// Экранирование не трогает точки, поэтому «..» и пустой id отвергаются до сети:
// сервер, нормализующий путь, выполнил бы по ним другой вызов.
func TestDotSegmentsAreRejected(t *testing.T) {
	ctx := context.Background()
	acc := testAccount()
	rec := newRecordingServer(t, `{}`)
	c := newTestClient(t, rec.URL)
	tgt := AmojoTarget{TLD: "ru", AccountKey: "demo", Secret: "s"}

	bad := map[string]func() error{
		"file uuid ..": func() error { _, err := c.GetFileLinks(ctx, acc, ".."); return err },
		"file uuid .":  func() error { _, err := c.GetFileLinks(ctx, acc, "."); return err },
		"file uuid empty": func() error {
			_, err := c.GetFileLinks(ctx, acc, "")
			return err
		},
		"unsorted uid ..":   func() error { return c.DeclineUnsorted(ctx, acc, "..") },
		"field group id ..": func() error { return c.DeleteCustomFieldGroup(ctx, acc, "leads", "..") },
		"amojo scope id ..": func() error { return c.AmojoTyping(ctx, tgt, "..", map[string]any{}) },
		"drive file uuid ..": func() error {
			_, err := c.PatchFile(ctx, acc, "https://drive-b.amocrm.ru", "..", PatchFileInput{})
			return err
		},
		"drive uuid . nested": func() error { _, err := c.GetFileVersions(ctx, acc, "https://drive-b.amocrm.ru", "."); return err },
	}
	for name, fn := range bad {
		if err := fn(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if rec.hits != 0 {
		t.Fatalf("server got %d requests with a dot segment in the path", rec.hits)
	}
}

// amoJo подписывает путь в том виде, в каком он уходит на провод.
func TestAmojoSignsEscapedPath(t *testing.T) {
	const secret = "s"
	rec := newRecordingServer(t, ``)
	c := newTestClient(t, rec.URL)

	tgt := AmojoTarget{TLD: "ru", AccountKey: "demo", Secret: secret}
	if err := c.AmojoTyping(context.Background(), tgt, "scope?x=1", map[string]any{}); err != nil {
		t.Fatalf("AmojoTyping: %v", err)
	}
	if rec.path != "/v2/origin/custom/scope%3Fx=1/typing" {
		t.Fatalf("path = %q", rec.path)
	}
	if rec.query != "" {
		t.Errorf("query = %q, want empty — the value must not leak into the query", rec.query)
	}
}

// Сущность в пути — только из allowlist; отказ перманентный (ErrInvalidRequest).
func TestEntityAllowlist(t *testing.T) {
	ctx := context.Background()
	acc := testAccount()
	rec := newRecordingServer(t, `{}`)
	c := newTestClient(t, rec.URL)

	bad := []func() error{
		func() error { _, err := c.ListTags(ctx, acc, "leads?x=1", ListParams{}); return err },
		func() error { _, err := c.CreateTags(ctx, acc, "../oauth2", nil); return err },
		func() error { _, err := c.UpdateTags(ctx, acc, "unknown", nil); return err },
		func() error { _, err := c.AddNotes(ctx, acc, "catalogs/1", nil); return err },
		func() error { return c.AttachFiles(ctx, acc, "leads/1", 1, nil) },
		func() error { return c.DetachFiles(ctx, acc, "", 1, nil) },
		func() error { _, err := c.ListCustomFields(ctx, acc, "catalogs/abc", ListParams{}); return err },
		func() error { _, err := c.ListCustomFieldGroups(ctx, acc, "catalogs/"); return err },
		func() error { return c.DeleteCustomField(ctx, acc, "leads/../x", 1) },
		func() error { _, err := c.CreateCustomFields(ctx, acc, "widgets", nil); return err },
		func() error { _, err := c.UpdateCustomFields(ctx, acc, "widgets", nil); return err },
		func() error { _, err := c.CreateCustomFieldGroups(ctx, acc, "widgets", nil); return err },
		func() error {
			_, err := c.UpdateCustomFieldGroup(ctx, acc, "widgets", "g", CustomFieldGroupInput{})
			return err
		},
	}
	for i, fn := range bad {
		if err := fn(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("case %d: err = %v, want ErrInvalidRequest", i, err)
		}
	}
	if rec.hits != 0 {
		t.Fatalf("server got %d requests with an entity outside the allowlist", rec.hits)
	}
}

// Связь вне матрицы amoCRM — перманентный отказ, а не transient-ошибка.
func TestLinkMatrixRejectionIsPermanent(t *testing.T) {
	c := newTestClient(t, "http://unused")
	ctx := context.Background()

	_, err := c.LinkEntities(ctx, testAccount(), "leads", 1, []LinkInput{{ToEntityID: 2, ToEntityType: "customers"}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
	if err := c.UnlinkEntities(ctx, testAccount(), "widgets", 1, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
	// contacts→leads amoCRM не принимает, companies→leads принимает: матрица асимметрична.
	if _, err := c.LinkEntities(ctx, testAccount(), "contacts", 1, []LinkInput{{ToEntityType: "leads"}}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("contacts→leads: err = %v, want ErrInvalidRequest", err)
	}
	if err := validateLinks("companies", []LinkInput{{ToEntityType: "leads"}}); err != nil {
		t.Errorf("companies→leads must be allowed: %v", err)
	}
}

// Невалидный URL — отказ до сети и без transient-класса.
func TestInvalidURLIsPermanent(t *testing.T) {
	c := newTestClient(t, "")
	_, err := c.CreateUploadSession(context.Background(), testAccount(), "drive-b.amocrm.ru", UploadSessionInput{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}
