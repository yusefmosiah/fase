package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yusefmosiah/cogent/internal/core"
)

// errServeBusy is returned when the serve responds with HTTP 409 (resource busy).
var errServeBusy = errors.New("resource busy")

// serveClient is a thin HTTP client that routes CLI commands through the
// running fase serve process. It reads .fase/serve.json for discovery.
type serveClient struct {
	baseURL    string
	httpClient *http.Client
}

// serveInfo mirrors the JSON written by runServe in serve.go.
type serveInfo struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	CWD       string `json:"cwd"`
	Auto      bool   `json:"auto"`
	EnvPath   string `json:"env_file,omitempty"`
	StateDir  string `json:"-"` // populated from the directory containing serve.json
}

// EnvFile returns the configured .env path if set.
func (s *serveInfo) EnvFile() (string, bool) {
	if s != nil && s.EnvPath != "" {
		return s.EnvPath, true
	}
	return "", false
}

// loadServeInfo reads .fase/serve.json, parses it, and verifies the PID is alive.
func loadServeInfo() (*serveInfo, error) {
	stateDir := core.ResolveRepoStateDir()
	if stateDir == "" {
		return nil, fmt.Errorf("fase serve is not running — start it with 'fase serve'")
	}
	info, err := loadServeInfoFrom(stateDir)
	if err != nil {
		return nil, err
	}
	info.StateDir = stateDir
	return info, nil
}

// loadServeInfoFrom reads serve.json from a specific state directory.
func loadServeInfoFrom(stateDir string) (*serveInfo, error) {
	path := filepath.Join(stateDir, "serve.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("fase serve is not running — start it with 'fase serve'")
		}
		return nil, fmt.Errorf("reading serve.json: %w", err)
	}

	var info serveInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parsing serve.json: %w", err)
	}
	if info.Port == 0 {
		return nil, fmt.Errorf("serve.json has no port — start fase serve")
	}

	// Verify PID is alive via kill(pid, 0).
	if info.PID > 0 {
		if err := syscall.Kill(info.PID, 0); err != nil {
			return nil, fmt.Errorf("fase serve is not running (stale serve.json, pid %d) — start it with 'fase serve'", info.PID)
		}
	}

	return &info, nil
}

// connectServe loads serve.json and returns a connected client.
func connectServe() (*serveClient, error) {
	info, err := loadServeInfo()
	if err != nil {
		return nil, err
	}
	return &serveClient{
		baseURL:    fmt.Sprintf("http://localhost:%d", info.Port),
		httpClient: &http.Client{},
	}, nil
}

// connectOrDie loads serve info or exits with a helpful message.
func connectOrDie() *serveClient {
	c, err := connectServe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return c
}

// apiError is the JSON error response from serve.
type apiError struct {
	Error string `json:"error"`
}

// doGet performs a GET request and returns the response body.
func (c *serveClient) doGet(path string, params url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	resp, err := c.httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("connecting to fase serve: %w", err)
	}
	defer resp.Body.Close()
	return c.handleResponse(resp)
}

// doPost performs a POST request with a JSON body and returns the response body.
func (c *serveClient) doPost(path string, body any) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}
	resp, err := c.httpClient.Post(c.baseURL+path, "application/json", reqBody)
	if err != nil {
		return nil, fmt.Errorf("connecting to fase serve: %w", err)
	}
	defer resp.Body.Close()
	return c.handleResponse(resp)
}

// doDelete performs a DELETE request with an optional JSON body.
func (c *serveClient) doDelete(path string, body any) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequest(http.MethodDelete, c.baseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to fase serve: %w", err)
	}
	defer resp.Body.Close()
	return c.handleResponse(resp)
}

// handleResponse reads the response body and checks for errors.
func (c *serveClient) handleResponse(resp *http.Response) ([]byte, error) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var ae apiError
		if json.Unmarshal(data, &ae) == nil && ae.Error != "" {
			// HTTP 409 or a "resource busy" message both indicate ErrBusy.
			if resp.StatusCode == 409 || strings.HasPrefix(ae.Error, "resource busy") {
				return nil, fmt.Errorf("%w: %s", errServeBusy, ae.Error)
			}
			return nil, fmt.Errorf("fase serve: %s", ae.Error)
		}
		return nil, fmt.Errorf("fase serve returned %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}
