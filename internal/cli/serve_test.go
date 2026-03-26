package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yusefmosiah/cogent/internal/service"
)

func newTestServeAPI(t *testing.T) (*service.Service, *httptest.Server) {
	t.Helper()
	t.Setenv("FASE_CONFIG_DIR", t.TempDir())
	t.Setenv("FASE_CACHE_DIR", t.TempDir())

	svc, err := service.OpenWithStateDir(context.Background(), "", t.TempDir())
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	mux := http.NewServeMux()
	registerAPIHandlers(mux, svc, t.TempDir(), nil, nil)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return svc, ts
}

func postJSON(t *testing.T, client *http.Client, url string, body any, out any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from %s, got %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode response from %s: %v", url, err)
	}
}

func TestWorkDocSetAPIHandlers(t *testing.T) {
	t.Run("auto create work when no work id is provided", func(t *testing.T) {
		svc, ts := newTestServeAPI(t)

		var resp struct {
			Doc struct {
				DocID  string `json:"doc_id"`
				WorkID string `json:"work_id"`
				Path   string `json:"path"`
				Title  string `json:"title"`
			} `json:"doc"`
			WorkID string `json:"work_id"`
		}
		postJSON(t, ts.Client(), ts.URL+"/api/work/doc-set", map[string]string{
			"path":   "docs/api-auto.md",
			"title":  "Auto API Doc",
			"body":   "# Auto API Doc\n",
			"format": "markdown",
		}, &resp)

		if resp.WorkID == "" || resp.Doc.WorkID != resp.WorkID {
			t.Fatalf("expected auto-created work id to round-trip, got work_id=%q doc=%+v", resp.WorkID, resp.Doc)
		}
		if resp.Doc.Path != "docs/api-auto.md" || resp.Doc.Title != "Auto API Doc" {
			t.Fatalf("unexpected auto-created doc payload: %+v", resp.Doc)
		}

		show, err := svc.Work(context.Background(), resp.WorkID)
		if err != nil {
			t.Fatalf("Work: %v", err)
		}
		if show.Work.Title != "Auto API Doc" {
			t.Fatalf("expected auto-created work title to match doc title, got %+v", show.Work)
		}
		if len(show.Docs) != 1 || show.Docs[0].Path != "docs/api-auto.md" {
			t.Fatalf("expected linked doc in auto-created work bundle, got %+v", show.Docs)
		}
	})

	t.Run("attach doc to existing work when work id is provided", func(t *testing.T) {
		svc, ts := newTestServeAPI(t)

		work, err := svc.CreateWork(context.Background(), service.WorkCreateRequest{
			Title:     "Existing Work",
			Objective: "Attach API doc",
			Kind:      "implement",
			CreatedBy: "test",
		})
		if err != nil {
			t.Fatalf("CreateWork: %v", err)
		}

		var resp struct {
			Doc struct {
				DocID  string `json:"doc_id"`
				WorkID string `json:"work_id"`
				Path   string `json:"path"`
			} `json:"doc"`
			WorkID string `json:"work_id"`
		}
		postJSON(t, ts.Client(), ts.URL+"/api/work/"+work.WorkID+"/doc-set", map[string]string{
			"path":   "docs/api-existing.md",
			"title":  "Existing API Doc",
			"body":   "# Existing API Doc\n",
			"format": "markdown",
		}, &resp)

		if resp.WorkID != work.WorkID || resp.Doc.WorkID != work.WorkID {
			t.Fatalf("expected existing work id to be preserved, got work_id=%q doc=%+v", resp.WorkID, resp.Doc)
		}
		if resp.Doc.Path != "docs/api-existing.md" {
			t.Fatalf("unexpected existing-work doc payload: %+v", resp.Doc)
		}

		show, err := svc.Work(context.Background(), work.WorkID)
		if err != nil {
			t.Fatalf("Work: %v", err)
		}
		if len(show.Docs) != 1 || show.Docs[0].Path != "docs/api-existing.md" {
			t.Fatalf("expected attached doc on existing work, got %+v", show.Docs)
		}
	})
}
