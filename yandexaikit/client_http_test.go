package yandexaikit

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
)

// testRSAKeyPEM генерит PKCS8 RSA-ключ в PEM для IAM-JWT тестов (PS256).
func testRSAKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// apiKeyClient — клиент с Api-Key auth, BaseURL указан на srv.
func apiKeyClient(t *testing.T, srvURL string, opts ...Option) *Client {
	t.Helper()
	hc := httpclient.New(httpclient.Config{})
	cfg := Config{APIKey: "test-key", BaseURL: srvURL, DefaultModel: "yandexgpt"}
	c, err := New(cfg, hc, clockkit.Real{}, nil, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCompletionHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Api-Key test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("x-folder-id"); got != "" {
			t.Errorf("x-folder-id must be empty with Api-Key, got %q", got)
		}
		if r.URL.Path != completionPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"result":{"alternatives":[{"message":{"role":"assistant","text":"hi"},"status":"ALTERNATIVE_STATUS_FINAL"}],"usage":{"inputTextTokens":"21","completionTokens":"17","totalTokens":"38"},"modelVersion":"23.10.2024"}}`)
	}))
	defer srv.Close()

	c := apiKeyClient(t, srv.URL)
	resp, err := c.Completion(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	if resp.Text() != "hi" {
		t.Fatalf("Text() = %q", resp.Text())
	}
	if resp.Usage.InputTokens != 21 || resp.Usage.OutputTokens != 17 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	// Pro 5 (yandexgpt) sync: ₽1.20/1k токенов → 1200 micro-RUB/токен.
	// (21+17)=38 токенов × 1200 = 45_600 micro-RUB.
	if resp.Cost != 45_600 {
		t.Fatalf("cost = %d, want 45600", resp.Cost)
	}
}

func TestCompletionErrorClassify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":16,"message":"Token is invalid or has expired."}`)
	}))
	defer srv.Close()

	c := apiKeyClient(t, srv.URL)
	_, err := c.Completion(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}})
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 16 {
		t.Fatalf("APIError not propagated: %v", err)
	}
}

func TestEmbeddingHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != embeddingPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"embedding":[0.1,0.2,0.3],"numTokens":"8","modelVersion":"06.12.2023"}`)
	}))
	defer srv.Close()

	c := apiKeyClient(t, srv.URL)
	res, err := c.TextEmbedding(context.Background(), EmbeddingInput{Model: "text-search-doc", Text: "q"})
	if err != nil {
		t.Fatalf("TextEmbedding: %v", err)
	}
	if len(res.Embedding) != 3 || res.Usage.InputTokens != 8 {
		t.Fatalf("result = %+v", res)
	}
}

func TestCompletionAsyncAndAwait(t *testing.T) {
	// Submit на BaseURL, poll на OperationURL — оба ведут на один httptest-сервер
	// с маршрутизацией по пути.
	var polls int
	mux := http.NewServeMux()
	mux.HandleFunc(completionAsyncPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"op-1","done":false}`)
	})
	mux.HandleFunc("/operations/op-1", func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls < 2 {
			_, _ = io.WriteString(w, `{"id":"op-1","done":false}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"op-1","done":true,"response":{"alternatives":[{"message":{"role":"assistant","text":"async"},"status":"ALTERNATIVE_STATUS_FINAL"}],"usage":{"inputTextTokens":"10","completionTokens":"5","totalTokens":"15"},"modelVersion":"v"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	hc := httpclient.New(httpclient.Config{})
	cfg := Config{APIKey: "k", BaseURL: srv.URL, OperationURL: srv.URL, DefaultModel: "yandexgpt"}
	c, err := New(cfg, hc, clockkit.Real{}, nil, WithPollConfig(pollConfig{
		Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 1, Timeout: time.Second,
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, err := c.CompletionAsync(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}})
	if err != nil || id != "op-1" {
		t.Fatalf("CompletionAsync id=%q err=%v", id, err)
	}
	resp, err := c.AwaitCompletion(context.Background(), id)
	if err != nil {
		t.Fatalf("AwaitCompletion: %v", err)
	}
	if resp.Text() != "async" || resp.Usage.OutputTokens != 5 {
		t.Fatalf("resp = %+v", resp)
	}
	// async-тариф: ₽0.61/1k токенов → 610 micro-RUB/токен.
	// (10+5)=15 токенов × 610 = 9_150 micro-RUB.
	if resp.Cost != 9_150 {
		t.Fatalf("async cost = %d, want 9150", resp.Cost)
	}
}

func TestImageGenerationAsyncAndAwait(t *testing.T) {
	imgB64 := base64.StdEncoding.EncodeToString([]byte("JPEGDATA"))
	mux := http.NewServeMux()
	mux.HandleFunc(imageGenerationAsyncPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"img-1","done":false}`)
	})
	mux.HandleFunc("/operations/img-1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"img-1","done":true,"response":{"image":"`+imgB64+`","modelVersion":"2024-10-10"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	hc := httpclient.New(httpclient.Config{})
	cfg := Config{APIKey: "k", BaseURL: srv.URL, OperationURL: srv.URL}
	c, err := New(cfg, hc, clockkit.Real{}, nil, WithPollConfig(pollConfig{Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 1, Timeout: time.Second}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, err := c.ImageGenerationAsync(context.Background(), ImageInput{Messages: []ImageMessage{{Text: "a panda", Weight: 1}}})
	if err != nil || id != "img-1" {
		t.Fatalf("ImageGenerationAsync id=%q err=%v", id, err)
	}
	res, err := c.AwaitImage(context.Background(), id)
	if err != nil {
		t.Fatalf("AwaitImage: %v", err)
	}
	if string(res.Image) != "JPEGDATA" || res.Usage.Images != 1 {
		t.Fatalf("res = %+v", res)
	}
	if res.Cost != 2_230_000 {
		t.Fatalf("image cost = %d, want 2230000", res.Cost)
	}
}

func TestImagePromptLimit(t *testing.T) {
	c := apiKeyClient(t, "http://unused")
	long := make([]rune, imagePromptLimit+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err := c.ImageGenerationAsync(context.Background(), ImageInput{Messages: []ImageMessage{{Text: string(long)}}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestOperationFailed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/operations/op-err", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"op-err","done":true,"error":{"code":13,"message":"internal"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	hc := httpclient.New(httpclient.Config{})
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL, OperationURL: srv.URL}, hc, clockkit.Real{}, nil,
		WithPollConfig(pollConfig{Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 1, Timeout: time.Second}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.AwaitCompletion(context.Background(), "op-err")
	if !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("err = %v, want ErrOperationFailed", err)
	}
}

func TestStreamCumulative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// native-стрим: поток полных {result} объектов (cumulative text).
		w.Header().Set("Content-Type", "application/json")
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`{"result":{"alternatives":[{"message":{"role":"assistant","text":"Hel"},"status":"ALTERNATIVE_STATUS_PARTIAL"}],"usage":{"inputTextTokens":"5","completionTokens":"1","totalTokens":"6"},"modelVersion":"v"}}`,
			`{"result":{"alternatives":[{"message":{"role":"assistant","text":"Hello"},"status":"ALTERNATIVE_STATUS_FINAL"}],"usage":{"inputTextTokens":"5","completionTokens":"2","totalTokens":"7"},"modelVersion":"v"}}`,
		}
		for _, ch := range chunks {
			_, _ = io.WriteString(w, ch+"\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()

	c := apiKeyClient(t, srv.URL)
	st := c.Stream(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}})
	defer st.Close()

	var lastText string
	var finalSeen bool
	for st.Next() {
		ev := st.Event()
		lastText = ev.Text // cumulative: перезаписываем
		if ev.Status.IsFinal() {
			finalSeen = true
		}
	}
	if err := st.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if !finalSeen || lastText != "Hello" {
		t.Fatalf("lastText=%q finalSeen=%v", lastText, finalSeen)
	}
	final, err := st.Final()
	if err != nil {
		t.Fatalf("Final: %v", err)
	}
	if final.Text() != "Hello" || final.Usage.OutputTokens != 2 {
		t.Fatalf("final = %+v", final)
	}
}

func TestIAMExchangeAndCompletion(t *testing.T) {
	// IAM-схема: x-folder-id обязателен, Authorization: Bearer <iam>; токен
	// добывается обменом PS256-JWT на /iam/v1/tokens.
	mux := http.NewServeMux()
	mux.HandleFunc("/iam/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !contains(string(body), `"jwt"`) {
			t.Errorf("IAM body missing jwt: %s", body)
		}
		_, _ = io.WriteString(w, `{"iamToken":"t1.iam","expiresAt":"`+time.Now().Add(12*time.Hour).Format(time.RFC3339)+`"}`)
	})
	mux.HandleFunc(completionPath, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer t1.iam" {
			t.Errorf("Authorization = %q, want Bearer t1.iam", got)
		}
		if got := r.Header.Get("x-folder-id"); got != "b1g" {
			t.Errorf("x-folder-id = %q, want b1g", got)
		}
		_, _ = io.WriteString(w, `{"result":{"alternatives":[{"message":{"text":"ok"},"status":"ALTERNATIVE_STATUS_FINAL"}],"usage":{"inputTextTokens":"1","completionTokens":"1","totalTokens":"2"},"modelVersion":"v"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	hc := httpclient.New(httpclient.Config{})
	cfg := Config{
		FolderID:            "b1g",
		ServiceAccountID:    "sa",
		ServiceAccountKeyID: "kid",
		PrivateKey:          testRSAKeyPEM(t),
		BaseURL:             srv.URL,
		IAMURL:              srv.URL,
		DefaultModel:        "yandexgpt",
	}
	c, err := New(cfg, hc, clockkit.Real{}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.Completion(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("Text() = %q", resp.Text())
	}
}

func TestIAMTokenCached(t *testing.T) {
	var exchanges int
	mux := http.NewServeMux()
	mux.HandleFunc("/iam/v1/tokens", func(w http.ResponseWriter, _ *http.Request) {
		exchanges++
		_, _ = io.WriteString(w, `{"iamToken":"t1","expiresAt":"`+time.Now().Add(12*time.Hour).Format(time.RFC3339)+`"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	hc := httpclient.New(httpclient.Config{})
	a, err := newIAMAuth(Config{
		FolderID: "b1g", ServiceAccountID: "sa", ServiceAccountKeyID: "kid",
		PrivateKey: testRSAKeyPEM(t), IAMURL: srv.URL,
	}, hc, clockkit.Real{})
	if err != nil {
		t.Fatalf("newIAMAuth: %v", err)
	}
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://x", nil)
		if err := a.apply(context.Background(), req); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	if exchanges != 1 {
		t.Fatalf("exchanges = %d, want 1 (token cached)", exchanges)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
