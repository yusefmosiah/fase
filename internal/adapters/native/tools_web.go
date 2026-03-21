package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
)

// WebSearchTool searches the web using multiple providers with rotation and fallback.
func WebSearchTool() Tool {
	return Tool{
		Name:        "web_search",
		Core:        true,
		Description: "Search the web for information. Returns titles, URLs, and text snippets from relevant pages.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query",
				},
				"num_results": map[string]any{
					"type":        "integer",
					"description": "Number of results to return (default 5, max 20)",
				},
			},
			"required": []string{"query"},
		},
		Func: toolFuncAdapter(execWebSearch),
	}
}

// WebFetchTool fetches the content of a web page.
func WebFetchTool() Tool {
	return Tool{
		Name:        "web_fetch",
		Description: "Fetch the text content of a web page by URL.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to fetch",
				},
			},
			"required": []string{"url"},
		},
		Func: toolFuncAdapter(execWebFetch),
	}
}

// searchProvider abstracts a web search API.
type searchProvider struct {
	name   string
	envKey string
	search func(ctx context.Context, apiKey, query string, numResults int) (string, error)
}

var searchProviders = []searchProvider{
	{"exa", "EXA_API_KEY", searchExa},
	{"tavily", "TAVILY_API_KEY", searchTavily},
	{"brave", "BRAVE_API_KEY", searchBrave},
	{"serper", "SERPER_API_KEY", searchSerper},
}

var searchRotation atomic.Int64

func execWebSearch(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Query      string `json:"query"`
		NumResults int    `json:"num_results"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if params.Query == "" {
		return "", fmt.Errorf("query must not be empty")
	}
	if params.NumResults <= 0 {
		params.NumResults = 5
	}
	if params.NumResults > 20 {
		params.NumResults = 20
	}

	// Collect available providers.
	var available []searchProvider
	for _, p := range searchProviders {
		if os.Getenv(p.envKey) != "" {
			available = append(available, p)
		}
	}
	if len(available) == 0 {
		return "", fmt.Errorf("no search API keys set (need one of: EXA_API_KEY, TAVILY_API_KEY, BRAVE_API_KEY, SERPER_API_KEY)")
	}

	// Round-robin starting point, then try each provider.
	start := int(searchRotation.Add(1)-1) % len(available)
	var lastErr error
	for i := range available {
		idx := (start + i) % len(available)
		p := available[idx]
		result, err := p.search(ctx, os.Getenv(p.envKey), params.Query, params.NumResults)
		if err == nil {
			return result, nil
		}
		lastErr = fmt.Errorf("%s: %w", p.name, err)
	}
	return "", fmt.Errorf("all search providers failed: %w", lastErr)
}

// --- Exa ---

func searchExa(ctx context.Context, apiKey, query string, numResults int) (string, error) {
	body := map[string]any{
		"query":      query,
		"numResults": numResults,
		"type":       "auto",
		"text":       true,
		"highlights": true,
		"livecrawl":  "always",
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.exa.ai/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	return doSearchRequest(req, parseExaResults)
}

func parseExaResults(data []byte) (string, error) {
	var result struct {
		Results []struct {
			Title      string   `json:"title"`
			URL        string   `json:"url"`
			Text       string   `json:"text"`
			Highlights []string `json:"highlights"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return string(data), nil
	}
	var b strings.Builder
	for i, r := range result.Results {
		text := strings.Join(r.Highlights, " ")
		if text == "" {
			text = r.Text
		}
		if len(text) > 500 {
			text = text[:500] + "..."
		}
		fmt.Fprintf(&b, "## %d. %s\nURL: %s\n%s\n\n", i+1, r.Title, r.URL, text)
	}
	return b.String(), nil
}

// --- Tavily ---

func searchTavily(ctx context.Context, apiKey, query string, numResults int) (string, error) {
	body := map[string]any{
		"query":       query,
		"max_results": numResults,
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	return doSearchRequest(req, parseTavilyResults)
}

func parseTavilyResults(data []byte) (string, error) {
	var result struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return string(data), nil
	}
	var b strings.Builder
	for i, r := range result.Results {
		fmt.Fprintf(&b, "## %d. %s\nURL: %s\n", i+1, r.Title, r.URL)
		text := r.Content
		if len(text) > 500 {
			text = text[:500] + "..."
		}
		fmt.Fprintf(&b, "%s\n\n", text)
	}
	return b.String(), nil
}

// --- Brave ---

func searchBrave(ctx context.Context, apiKey, query string, numResults int) (string, error) {
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), numResults)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	return doSearchRequest(req, parseBraveResults)
}

func parseBraveResults(data []byte) (string, error) {
	var result struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return string(data), nil
	}
	var b strings.Builder
	for i, r := range result.Web.Results {
		fmt.Fprintf(&b, "## %d. %s\nURL: %s\n%s\n\n", i+1, r.Title, r.URL, r.Description)
	}
	return b.String(), nil
}

// --- Serper ---

func searchSerper(ctx context.Context, apiKey, query string, numResults int) (string, error) {
	body := map[string]any{
		"q":   query,
		"num": numResults,
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.serper.dev/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)

	return doSearchRequest(req, parseSerperResults)
}

func parseSerperResults(data []byte) (string, error) {
	var result struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return string(data), nil
	}
	var b strings.Builder
	for i, r := range result.Organic {
		fmt.Fprintf(&b, "## %d. %s\nURL: %s\n%s\n\n", i+1, r.Title, r.Link, r.Snippet)
	}
	return b.String(), nil
}

// --- Shared ---

func doSearchRequest(req *http.Request, parse func([]byte) (string, error)) (string, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return parse(body)
}

func execWebFetch(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if params.URL == "" {
		return "", fmt.Errorf("url must not be empty")
	}

	parsed, err := url.Parse(params.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid URL: %s", params.URL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "fase-native-adapter/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch %s: status %s", params.URL, resp.Status)
	}

	return string(body), nil
}
