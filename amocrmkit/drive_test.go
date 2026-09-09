package amocrmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Файл больше max_part_size грузится частями: каждая следующая уходит на
// next_url предыдущего ответа, финальный ответ несёт UUID.
func TestUploadFile_FollowsNextURL(t *testing.T) {
	type part struct {
		path  string
		rng   string
		bytes []byte
	}
	var got []part

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1.0/sessions":
			var in UploadSessionInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.FileSize != 10 || in.FileName != "f.bin" {
				t.Errorf("session input = %+v", in)
			}
			_, _ = fmt.Fprintf(w, `{"upload_url":%q,"max_part_size":4,"file_uuid":"u-1"}`, srv.URL+"/upload/1")
		case strings.HasPrefix(r.URL.Path, "/upload/"):
			body, _ := io.ReadAll(r.Body)
			got = append(got, part{path: r.URL.Path, rng: r.Header.Get("Content-Range"), bytes: body})
			if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("Content-Type = %q", ct)
			}
			switch len(got) {
			case 1:
				_, _ = fmt.Fprintf(w, `{"session_id":"s-1","next_url":%q}`, srv.URL+"/upload/2")
			case 2:
				_, _ = fmt.Fprintf(w, `{"session_id":"s-1","next_url":%q}`, srv.URL+"/upload/3")
			default:
				_, _ = w.Write([]byte(`{"uuid":"u-1","name":"f.bin","size":10}`))
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	file, err := c.UploadFile(context.Background(), testAccount(), driveBase,
		UploadSessionInput{FileName: "f.bin", FileSize: 10}, bytes.NewReader([]byte("0123456789")))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if file.UUID != "u-1" || file.Size != 10 {
		t.Fatalf("file = %+v", file)
	}
	if len(got) != 3 {
		t.Fatalf("parts = %d, want 3", len(got))
	}
	want := []part{
		{path: "/upload/1", rng: "bytes 0-3/10", bytes: []byte("0123")},
		{path: "/upload/2", rng: "bytes 4-7/10", bytes: []byte("4567")},
		{path: "/upload/3", rng: "bytes 8-9/10", bytes: []byte("89")},
	}
	for i, w := range want {
		if got[i].path != w.path || got[i].rng != w.rng || !bytes.Equal(got[i].bytes, w.bytes) {
			t.Errorf("part %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// Файл в одну часть: сессия сразу отдаёт финальный File.
func TestUploadFile_SinglePart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0/sessions" {
			_, _ = w.Write([]byte(`{"upload_url":"https://drive-b.amocrm.ru/upload/1","max_part_size":1024}`))
			return
		}
		_, _ = w.Write([]byte(`{"uuid":"u-2","size":3}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	file, err := c.UploadFile(context.Background(), testAccount(), driveBase,
		UploadSessionInput{FileName: "f", FileSize: 3}, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if file.UUID != "u-2" {
		t.Errorf("file = %+v", file)
	}
}

// Сессия без max_part_size — контракт сервера нарушен, грузить нечем.
func TestUploadFile_SessionWithoutPartSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"upload_url":"https://drive-b.amocrm.ru/upload/1"}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	_, err := c.UploadFile(context.Background(), testAccount(), driveBase,
		UploadSessionInput{FileName: "f", FileSize: 3}, strings.NewReader("abc"))
	if !errors.Is(err, ErrServer) {
		t.Fatalf("err = %v, want ErrServer", err)
	}
}

// Reader короче file_size: сервер ждёт часть, которую нечем наполнить.
func TestUploadFile_ReaderShorterThanFileSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0/sessions" {
			_, _ = w.Write([]byte(`{"upload_url":"https://drive-b.amocrm.ru/upload/1","max_part_size":4}`))
			return
		}
		_, _ = w.Write([]byte(`{"session_id":"s-1","next_url":"https://drive-b.amocrm.ru/upload/2"}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	_, err := c.UploadFile(context.Background(), testAccount(), driveBase,
		UploadSessionInput{FileName: "f", FileSize: 10}, strings.NewReader("0123"))
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

// drive_url и upload_url приходят снаружи: Bearer аккаунта не уходит на чужой хост.
func TestDriveHostAllowlist(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	// Без HostOverride: адрес Drive проверяется по домену аккаунта.
	c := newTestClientCfg(t, Config{RateRPS: 1000}, testHTTPConfig())
	ctx := context.Background()
	acc := testAccount()

	evil := []func() error{
		func() error { _, err := c.CreateUploadSession(ctx, acc, srv.URL, UploadSessionInput{}); return err },
		func() error {
			_, err := c.CreateUploadSession(ctx, acc, "https://evil.example", UploadSessionInput{})
			return err
		},
		func() error {
			_, err := c.CreateUploadSession(ctx, acc, "http://drive-b.amocrm.ru", UploadSessionInput{})
			return err
		},
		func() error {
			_, err := c.CreateUploadSession(ctx, acc, "https://drive-b.amocrm.com", UploadSessionInput{})
			return err
		},
		func() error {
			_, err := c.CreateUploadSession(ctx, acc, "https://amocrm.ru.evil.example", UploadSessionInput{})
			return err
		},
		func() error { _, err := c.UploadPart(ctx, acc, "https://evil.example/upload", "", nil); return err },
		func() error { _, err := c.ListFiles(ctx, acc, "https://evil.example", ListParams{}); return err },
	}
	for i, fn := range evil {
		if err := fn(); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("case %d: err = %v, want ErrInvalidAccount", i, err)
		}
	}
	if hits != 0 {
		t.Fatalf("server got %d requests to a foreign host", hits)
	}
}
