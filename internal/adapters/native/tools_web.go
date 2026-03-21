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
)

// WebSearchTool searches the web using the Exa API.
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

	apiKey := os.Getenv("EXA_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("EXA_API_KEY not set")
	}

	body := map[string]any{
		"query":       params.Query,
		"numResults":  params.NumResults,
		"type":        "auto",
		"text":        true,
		"highlights":  true,
		"livecrawl":   "always",
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.exa.ai/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exa search: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("exa search: status %s: %s", resp.Status, string(respBody))
	}

	// Parse and format results
	var result struct {
		Results []struct {
			Title      string `json:"title"`
			URL        string `json:"url"`
			Text       string `json:"text"`
			Highlights []string `json:"highlights"`
		} `json:"results"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return string(respBody), nil // return raw if can't parse
	}

	var b strings.Builder
	for i, r := range result.Results {
		fmt.Fprintf(&b, "## %d. %s\n", i+1, r.Title)
		fmt.Fprintf(&b, "URL: %s\n", r.URL)
		if len(r.Highlights) > 0 {
			for _, h := range r.Highlights {
				fmt.Fprintf(&b, "> %s\n", h)
			}
		} else if r.Text != "" {
			text := r.Text
			if len(text) > 500 {
				text = text[:500] + "..."
			}
			fmt.Fprintf(&b, "%s\n", text)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
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

	// Validate URL
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
