package native

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolRegistryDefinitionsAndExecute(t *testing.T) {
	t.Parallel()

	registry, err := NewToolRegistry(
		toolFromFunc("b_tool", "second", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
			return "b", nil
		}),
		toolFromFunc("a_tool", "first", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
			return "a", nil
		}),
	)
	if err != nil {
		t.Fatalf("NewToolRegistry returned error: %v", err)
	}

	defs := registry.Definitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 tool defs, got %d", len(defs))
	}
	if defs[0].Name != "a_tool" || defs[1].Name != "b_tool" {
		t.Fatalf("definitions not sorted: %+v", defs)
	}
	if _, ok := registry.Lookup("a_tool"); !ok {
		t.Fatal("expected lookup for registered tool to succeed")
	}
	if _, ok := registry.Lookup("missing"); ok {
		t.Fatal("expected lookup for missing tool to fail")
	}

	anthropic := registry.AnthropicDefinitions()
	if len(anthropic) != 2 || anthropic[0].Name != "a_tool" || anthropic[0].InputSchema["type"] != "object" {
		t.Fatalf("unexpected anthropic definitions: %+v", anthropic)
	}
	openAI := registry.OpenAIDefinitions()
	if len(openAI) != 2 || openAI[0].Type != "function" {
		t.Fatalf("unexpected openai definitions: %+v", openAI)
	}
	if got := openAI[0].Parameters["type"]; got != "object" {
		t.Fatalf("expected default schema object, got %v", got)
	}
	if props, ok := openAI[0].Parameters["properties"].(map[string]any); !ok || len(props) != 0 {
		t.Fatalf("expected empty properties map for parameterless tool, got %+v", openAI[0].Parameters["properties"])
	}
	openAI[0].Parameters["mutated"] = true
	if defsAgain := registry.Definitions(); defsAgain[0].Parameters["mutated"] != nil {
		t.Fatalf("definitions should be cloned, got %+v", defsAgain[0].Parameters)
	}

	out, err := registry.Execute(context.Background(), "a_tool", nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if out != "a" {
		t.Fatalf("unexpected tool output: %q", out)
	}
	if _, err := registry.Execute(context.Background(), "missing", nil); err == nil {
		t.Fatal("expected missing tool execution to fail")
	}
}

func TestToolRegistryRegisterRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	registry := MustNewToolRegistry(toolFromFunc("dupe", "first", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		return "ok", nil
	}))

	err := registry.Register(toolFromFunc("dupe", "second", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		return "nope", nil
	}))
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate registration error, got %v", err)
	}
}

func TestCodingToolsRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registry := MustNewToolRegistry()
	if err := RegisterCodingTools(registry, dir); err != nil {
		t.Fatalf("RegisterCodingTools returned error: %v", err)
	}

	fileArgs := mustJSON(t, map[string]any{
		"path":    "nested/example.txt",
		"content": "hello world\nhello team\n",
	})
	if _, err := registry.Execute(context.Background(), "write_file", fileArgs); err != nil {
		t.Fatalf("write_file returned error: %v", err)
	}

	editArgs := mustJSON(t, map[string]any{
		"path":       "nested/example.txt",
		"old_string": "hello world",
		"new_string": "hello registry",
	})
	if _, err := registry.Execute(context.Background(), "edit_file", editArgs); err != nil {
		t.Fatalf("edit_file returned error: %v", err)
	}

	readOut, err := registry.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
		"path": filepath.Join("nested", "example.txt"),
	}))
	if err != nil {
		t.Fatalf("read_file returned error: %v", err)
	}
	if !strings.Contains(readOut, "hello registry") {
		t.Fatalf("read_file output missing edited content: %s", readOut)
	}

	globOut, err := registry.Execute(context.Background(), "glob", mustJSON(t, map[string]any{
		"pattern": "**/*.txt",
	}))
	if err != nil {
		t.Fatalf("glob returned error: %v", err)
	}
	if !strings.Contains(globOut, "nested/example.txt") {
		t.Fatalf("glob output missing expected file: %s", globOut)
	}

	grepOut, err := registry.Execute(context.Background(), "grep", mustJSON(t, map[string]any{
		"pattern": "registry",
	}))
	if err != nil {
		t.Fatalf("grep returned error: %v", err)
	}
	if !strings.Contains(grepOut, "nested/example.txt") {
		t.Fatalf("grep output missing expected file: %s", grepOut)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	return data
}
