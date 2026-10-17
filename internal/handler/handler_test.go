package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HoshinoNeko/YATFHS/internal/config"
	"github.com/HoshinoNeko/YATFHS/internal/middleware"
	"github.com/HoshinoNeko/YATFHS/internal/storage"
)

const testToken = "test-admin-token-with-32-characters-minimum"

func fixture(t *testing.T) (*Handler, *config.Config) {
	t.Helper()
	cfg := config.Default()
	cfg.StorageDir = t.TempDir()
	cfg.AdminToken = testToken
	return reopen(t, cfg), cfg
}
func reopen(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()
	s, err := storage.New(cfg.StorageDir)
	if err != nil {
		t.Fatal(err)
	}
	l := middleware.NewLimiter(10000, 10000, 1<<40, nil)
	h, err := New(cfg, s, l)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func request(h http.Handler, method, path string, body io.Reader, token, ct string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://files.example"+path, body)
	r.RemoteAddr = "192.0.2.1:1234"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func jsonRequest(h http.Handler, method, path string, data any, token string) *httptest.ResponseRecorder {
	var body io.Reader
	if data != nil {
		b, _ := json.Marshal(data)
		body = bytes.NewReader(b)
	}
	return request(h, method, path, body, token, "application/json")
}
func dataOf[T any](t *testing.T, w *httptest.ResponseRecorder, code int) T {
	t.Helper()
	if w.Code != code {
		t.Fatalf("HTTP %d, wanted %d: %s", w.Code, code, w.Body.String())
	}
	var e struct {
		Status string `json:"status"`
		Data   T      `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e.Data
}
func upload(t *testing.T, h http.Handler, name string, body []byte, expiry, token string) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	f, err := m.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(body)
	if expiry != "" {
		m.WriteField("expire", expiry)
	}
	m.Close()
	return request(h, "POST", "/api/v1/upload", &b, token, m.FormDataContentType())
}
func TestUploadExpiryDownloadAndFilename(t *testing.T) {
	h, _ := fixture(t)
	routes := h.Routes()
	body := []byte("file contents")
	data := dataOf[uploadData](t, upload(t, routes, "meta.json", body, "0", testToken), 200)
	if data.ExpiresAt != nil {
		t.Fatal("permanent upload has expiry")
	}
	if !strings.HasPrefix(data.URL, "https://files.example/") {
		t.Fatal(data.URL)
	}
	u, _ := url.Parse(data.URL)
	w := request(routes, "GET", u.EscapedPath(), nil, "", "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("download: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Expires-At") != "" {
		t.Fatal("permanent expiry header")
	}
	r := httptest.NewRequest("GET", data.URL, nil)
	r.Header.Set("Range", "bytes=0-3")
	w = httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "file" {
		t.Fatalf("range: %d %s", w.Code, w.Body.String())
	}
	if w = request(routes, "HEAD", u.EscapedPath(), nil, "", ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD failed")
	}
	if w = upload(t, routes, "test.txt", body, "0", ""); w.Code != 400 {
		t.Fatal("public permanent upload accepted")
	}
	if w = upload(t, routes, "test.txt", body, "0", "wrong"); w.Code != 400 {
		t.Fatal("bad token bypassed expiry")
	}
	if w = upload(t, routes, "test.txt", body, "172801", testToken); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	encoded := dataOf[uploadData](t, upload(t, routes, "中文 #?.txt", body, "60", ""), 200)
	u, _ = url.Parse(encoded.URL)
	w = request(routes, "GET", u.EscapedPath(), nil, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = request(routes, "GET", "/"+data.ID+"/other-name", nil, "", ""); w.Code != 404 {
		t.Fatal("filename mismatch accepted")
	}
}
func TestAdminPolicyPersistenceAndAuthorization(t *testing.T) {
	h, cfg := fixture(t)
	routes := h.Routes()
	for _, token := range []string{"", "wrong"} {
		if w := jsonRequest(routes, "GET", "/api/v1/admin/overview", nil, token); w.Code != 401 {
			t.Fatalf("unauthorized overview %d", w.Code)
		}
	}
	p := config.ExpiryPolicy{MinExpireSec: 120, DefaultExpireSec: 180, MaxExpireSec: 600, ExpireOptionsSec: []int{120, 180, 600}}
	dataOf[config.ExpiryPolicy](t, jsonRequest(routes, "PUT", "/api/v1/admin/settings", p, testToken), 200)
	if w := upload(t, routes, "file", []byte("data"), "60", ""); w.Code != 400 {
		t.Fatal("old range still accepted")
	}
	result := dataOf[uploadData](t, upload(t, routes, "file", []byte("data"), "", ""), 200)
	if result.ExpiresAt == nil {
		t.Fatal("default not applied")
	}
	p.ExpireOptionsSec = []int{0}
	if w := jsonRequest(routes, "PUT", "/api/v1/admin/settings", p, testToken); w.Code != 400 {
		t.Fatal("invalid options accepted")
	}
	h = reopen(t, cfg)
	if h.getPolicy().DefaultExpireSec != 180 {
		t.Fatal("policy did not survive restart")
	}
	overview := dataOf[struct {
		Logs []json.RawMessage `json:"logs"`
	}](t, jsonRequest(h.Routes(), "GET", "/api/v1/admin/overview", nil, testToken), 200)
	if len(overview.Logs) < 2 {
		t.Fatal("logs did not survive restart")
	}
	cfg.AdminToken = ""
	h = reopen(t, cfg)
	if w := jsonRequest(h.Routes(), "GET", "/api/v1/admin/files", nil, testToken); w.Code != 503 {
		t.Fatal("disabled admin accepted token")
	}
}

type sessionData struct {
	ID     string      `json:"id"`
	Chunks []int       `json:"uploaded_chunks"`
	Result *uploadData `json:"result"`
}

func createSession(t *testing.T, routes http.Handler, name string, size int64, expire int, token string) sessionData {
	t.Helper()
	return dataOf[sessionData](t, jsonRequest(routes, "POST", "/api/v1/uploads", map[string]any{"name": name, "size": size, "expire": expire, "fingerprint": "content-identity"}, token), 200)
}
func TestChunksResumeAcrossRestartAndIdempotentCompletion(t *testing.T) {
	h, cfg := fixture(t)
	routes := h.Routes()
	body := bytes.Repeat([]byte("x"), int(chunkSize)+7)
	copy(body[len(body)-7:], []byte("last123"))
	session := createSession(t, routes, "resume.bin", int64(len(body)), 60, "")
	path := "/api/v1/uploads/" + session.ID
	if w := request(routes, "PUT", path+"/chunks/1", strings.NewReader("bad"), "", ""); w.Code != 400 {
		t.Fatal("short chunk accepted")
	}
	dataOf[any](t, request(routes, "PUT", path+"/chunks/1", bytes.NewReader(body[chunkSize:]), "", ""), 200)
	if w := jsonRequest(routes, "POST", path+"/complete", nil, ""); w.Code != 409 {
		t.Fatal("incomplete upload accepted")
	}
	h = reopen(t, cfg)
	routes = h.Routes()
	status := dataOf[sessionData](t, jsonRequest(routes, "GET", path, nil, ""), 200)
	if len(status.Chunks) != 1 || status.Chunks[0] != 1 {
		t.Fatalf("lost completed chunk: %+v", status)
	}
	dataOf[any](t, request(routes, "PUT", path+"/chunks/0", bytes.NewReader(body[:chunkSize]), "", ""), 200)
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- jsonRequest(routes, "POST", path+"/complete", nil, "") }()
	}
	wg.Wait()
	close(results)
	id := ""
	var result uploadData
	for w := range results {
		result = dataOf[uploadData](t, w, 200)
		if id != "" && id != result.ID {
			t.Fatal("duplicate files on concurrent completion")
		}
		id = result.ID
	}
	u, _ := url.Parse(result.URL)
	w := request(routes, "GET", u.EscapedPath(), nil, "", "")
	if !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatal("assembled content mismatch")
	}
	if n, _ := h.store.Stats(); n != 1 {
		t.Fatal("duplicate stored files")
	}
	h = reopen(t, cfg)
	status = dataOf[sessionData](t, jsonRequest(h.Routes(), "GET", path, nil, ""), 200)
	if status.Result == nil || status.Result.ID != id {
		t.Fatal("completion lost across restart")
	}
}
func TestChunksAdminPolicyAndCancel(t *testing.T) {
	h, _ := fixture(t)
	routes := h.Routes()
	admin := createSession(t, routes, "forever", 0, 0, testToken)
	path := "/api/v1/uploads/" + admin.ID
	for _, verb := range []string{"GET", "DELETE"} {
		if w := jsonRequest(routes, verb, path, nil, ""); w.Code != 401 {
			t.Fatal("admin session accessible without token")
		}
	}
	if w := jsonRequest(routes, "POST", path+"/complete", nil, ""); w.Code != 401 {
		t.Fatal("public completed admin upload")
	}
	result := dataOf[uploadData](t, jsonRequest(routes, "POST", path+"/complete", nil, testToken), 200)
	if result.ExpiresAt != nil || result.Size != 0 {
		t.Fatal("empty permanent file failed")
	}
	session := createSession(t, routes, "file", 0, 60, "")
	path = "/api/v1/uploads/" + session.ID
	p := config.ExpiryPolicy{MinExpireSec: 120, DefaultExpireSec: 180, MaxExpireSec: 600, ExpireOptionsSec: []int{120}}
	dataOf[any](t, jsonRequest(routes, "PUT", "/api/v1/admin/settings", p, testToken), 200)
	if w := jsonRequest(routes, "POST", path+"/complete", nil, ""); w.Code != 400 {
		t.Fatal("new expiry policy bypassed")
	}
	dataOf[any](t, jsonRequest(routes, "DELETE", path, nil, ""), 200)
	if w := jsonRequest(routes, "GET", path, nil, ""); w.Code != 410 {
		t.Fatal("cancelled session still live")
	}
	if w := jsonRequest(routes, "POST", "/api/v1/uploads", map[string]any{"name": "x", "size": 1, "expire": 0}, ""); w.Code != 400 {
		t.Fatal("public permanent session accepted")
	}
}
func TestArchiveJSONAndNativeForm(t *testing.T) {
	h, _ := fixture(t)
	routes := h.Routes()
	first := dataOf[uploadData](t, upload(t, routes, "same.txt", []byte("first"), "60", ""), 200)
	second := dataOf[uploadData](t, upload(t, routes, "same.txt", []byte("second"), "60", ""), 200)
	ids := []string{first.ID, second.ID, first.ID}
	b, _ := json.Marshal(ids)
	for _, form := range []bool{false, true} {
		var w *httptest.ResponseRecorder
		if form {
			w = request(routes, "POST", "/api/v1/downloads", strings.NewReader(url.Values{"ids": {string(b)}}.Encode()), "", "application/x-www-form-urlencoded")
		} else {
			w = jsonRequest(routes, "POST", "/api/v1/downloads", map[string]any{"ids": ids}, "")
		}
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		if len(z.File) != 2 || z.File[0].Name == z.File[1].Name {
			t.Fatal("ZIP duplicates")
		}
		for i, f := range z.File {
			r, _ := f.Open()
			data, _ := io.ReadAll(r)
			r.Close()
			if string(data) != []string{"first", "second"}[i] {
				t.Fatal("ZIP content mismatch")
			}
		}
	}
	if w := jsonRequest(routes, "POST", "/api/v1/downloads", map[string]any{"ids": []string{first.ID, "../../settings.json"}}, ""); w.Code != 404 {
		t.Fatal("unknown file accepted")
	}
}
func TestUploadLimitsAndChunkQuota(t *testing.T) {
	h, _ := fixture(t)
	h.cfg.MaxFileSizeBytes = 5
	if w := upload(t, h.Routes(), "file", []byte("123456"), "60", ""); w.Code != 413 {
		t.Fatal("oversized file accepted")
	}
	if w := jsonRequest(h.Routes(), "POST", "/api/v1/uploads", map[string]any{"name": "x", "size": 6}, ""); w.Code != 400 {
		t.Fatal("oversized session accepted")
	}
	h.cfg.MaxFileSizeBytes = 10
	h.limiter = middleware.NewLimiter(1, 10, 5, nil)
	routes := h.Routes()
	s := createSession(t, routes, "small", 5, 60, "")
	path := "/api/v1/uploads/" + s.ID
	for i := 0; i < 2; i++ {
		dataOf[any](t, request(routes, "PUT", path+"/chunks/0", strings.NewReader("12345"), "", ""), 200)
	}
	dataOf[uploadData](t, jsonRequest(routes, "POST", path+"/complete", nil, ""), 200)
	if w := jsonRequest(routes, "POST", "/api/v1/uploads", map[string]any{"name": "x", "size": 1}, ""); w.Code != 429 {
		t.Fatal("upload request quota bypassed")
	}
}
func TestConcurrentSettingsAndStatus(t *testing.T) {
	h, _ := fixture(t)
	routes := h.Routes()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				p := config.ExpiryPolicy{MinExpireSec: 60, DefaultExpireSec: 60, MaxExpireSec: 600, ExpireOptionsSec: []int{60, 600}}
				w := jsonRequest(routes, "PUT", "/api/v1/admin/settings", p, testToken)
				if w.Code != 200 {
					t.Error(w.Body.String())
				}
			} else {
				w := jsonRequest(routes, "GET", "/api/v1/stats", nil, "")
				if w.Code != 200 {
					t.Error(fmt.Sprint(w.Code))
				}
			}
		}(i)
	}
	wg.Wait()
}

// A slow sender must not hold the global session registry and stall other uploads.
func TestSlowChunkDoesNotBlockSessionCreation(t *testing.T) {
	h, _ := fixture(t)
	routes := h.Routes()
	session := createSession(t, routes, "slow", 1, 60, "")
	reader, writer := io.Pipe()
	started := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	wrapped := &signalReader{reader: reader, started: started}
	go func() { done <- request(routes, "PUT", "/api/v1/uploads/"+session.ID+"/chunks/0", wrapped, "", "") }()
	<-started
	created := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		created <- jsonRequest(routes, "POST", "/api/v1/uploads", map[string]any{"name": "next", "size": 0, "expire": 60}, "")
	}()
	select {
	case w := <-created:
		if w.Code != 200 {
			t.Error(w.Body.String())
		}
	case <-time.After(time.Second):
		t.Error("session creation blocked by slow chunk")
	}
	writer.Write([]byte("x"))
	writer.Close()
	if w := <-done; w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

type signalReader struct {
	reader  io.Reader
	started chan struct{}
	once    sync.Once
}

func (r *signalReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.reader.Read(p)
}
