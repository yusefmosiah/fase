package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func newTestClient(t *testing.T, handler http.Handler) *serveClient {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &serveClient{
		baseURL:    ts.URL,
		httpClient: ts.Client(),
	}
}

func TestDoGet(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Query().Get("kind") != "implement" {
			t.Errorf("expected kind=implement, got %q", r.URL.Query().Get("kind"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))

	params := url.Values{"kind": []string{"implement"}}
	data, err := c.doGet("/api/work/list", params)
	if err != nil {
		t.Fatalf("doGet: %v", err)
	}

	var resp map[string]string
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected ok, got %q", resp["status"])
	}
}

func TestDoPost(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json content-type")
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"title": body["title"]})
	}))

	data, err := c.doPost("/api/work/create", map[string]string{"title": "test item"})
	if err != nil {
		t.Fatalf("doPost: %v", err)
	}

	var resp map[string]string
	json.Unmarshal(data, &resp)
	if resp["title"] != "test item" {
		t.Fatalf("expected 'test item', got %q", resp["title"])
	}
}

func TestDoDelete(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"deleted": "true"})
	}))

	data, err := c.doDelete("/api/work/edges", map[string]string{"from": "a", "to": "b"})
	if err != nil {
		t.Fatalf("doDelete: %v", err)
	}

	var resp map[string]string
	json.Unmarshal(data, &resp)
	if resp["deleted"] != "true" {
		t.Fatalf("expected deleted=true")
	}
}

func TestDoGetErrorResponse(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(apiError{Error: "work item not found"})
	}))

	_, err := c.doGet("/api/work/nonexistent", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "fase serve: work item not found" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoGetNonJSONError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))

	_, err := c.doGet("/api/fail", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadServeInfoFrom(t *testing.T) {
	dir := t.TempDir()

	// No serve.json → clear error
	_, err := loadServeInfoFrom(dir)
	if err == nil {
		t.Fatal("expected error for missing serve.json")
	}

	// Write serve.json with current PID so kill(pid, 0) succeeds
	info := serveInfo{PID: os.Getpid(), Port: 9999, CWD: dir}
	data, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(dir, "serve.json"), data, 0o644)

	got, err := loadServeInfoFrom(dir)
	if err != nil {
		t.Fatalf("loadServeInfoFrom: %v", err)
	}
	if got.Port != 9999 {
		t.Fatalf("expected port 9999, got %d", got.Port)
	}

	// Stale PID → error
	info.PID = 99999999
	data, _ = json.Marshal(info)
	os.WriteFile(filepath.Join(dir, "serve.json"), data, 0o644)

	_, err = loadServeInfoFrom(dir)
	if err == nil {
		t.Fatal("expected error for stale PID")
	}
}
