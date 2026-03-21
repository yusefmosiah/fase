package native

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ToolFunc is the common execution contract for native tools.
type ToolFunc interface {
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

type toolFuncAdapter func(ctx context.Context, args json.RawMessage) (string, error)

func (f toolFuncAdapter) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return f(ctx, args)
}

// Tool describes a callable tool plus its LLM-facing schema metadata.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Core        bool           `json:"-"` // core tools get full schemas in first call
	Func        ToolFunc       `json:"-"`
}

func (t Tool) Validate() error {
	if t.Name == "" {
		return fmt.Errorf("tool name must not be empty")
	}
	if t.Func == nil {
		return fmt.Errorf("tool %q has nil func", t.Name)
	}
	return nil
}

func (t Tool) Definition() ToolDef {
	return ToolDef{
		Name:        t.Name,
		Description: t.Description,
		Parameters:  cloneSchemaMap(t.Parameters),
	}
}

func (t Tool) AnthropicDefinition() anthropicTool {
	return anthropicTool{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: cloneSchemaMap(t.Parameters),
	}
}

func (t Tool) OpenAIDefinition() openAITool {
	return openAITool{
		Type:        "function",
		Name:        t.Name,
		Description: t.Description,
		Parameters:  cloneSchemaMap(t.Parameters),
	}
}

type ToolRegistry struct {
	tools map[string]Tool
	order []string
}

func NewToolRegistry(tools ...Tool) (*ToolRegistry, error) {
	r := &ToolRegistry{tools: make(map[string]Tool, len(tools))}
	for _, tool := range tools {
		if err := r.Register(tool); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func MustNewToolRegistry(tools ...Tool) *ToolRegistry {
	r, err := NewToolRegistry(tools...)
	if err != nil {
		panic(err)
	}
	return r
}

func (r *ToolRegistry) Register(tool Tool) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	if err := tool.Validate(); err != nil {
		return err
	}
	if len(tool.Parameters) == 0 {
		tool.Parameters = jsonSchemaObject(nil, nil, false)
	}
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("tool %q already registered", tool.Name)
	}
	r.tools[tool.Name] = tool
	r.order = append(r.order, tool.Name)
	sort.Strings(r.order)
	return nil
}

func (r *ToolRegistry) MustRegister(tool Tool) {
	if err := r.Register(tool); err != nil {
		panic(err)
	}
}

func (r *ToolRegistry) Lookup(name string) (Tool, bool) {
	if r == nil {
		return Tool{}, false
	}
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	tool, ok := r.Lookup(name)
	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	return tool.Func.Execute(ctx, args)
}

func (r *ToolRegistry) Tools() []Tool {
	if r == nil {
		return nil
	}
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Catalog returns a compact one-line-per-tool description suitable for
// inclusion in the system prompt. No JSON schemas — just names and descriptions.
// The LLM reads this to know what tools are available, then calls them by name.
func (r *ToolRegistry) Catalog() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available tools:\n")
	for _, name := range r.order {
		tool := r.tools[name]
		desc := tool.Description
		if len(desc) > 80 {
			desc = desc[:80] + "..."
		}
		fmt.Fprintf(&b, "- %s — %s\n", name, desc)
	}
	return b.String()
}

// CoreDefinitions returns full schemas for core tools only.
func (r *ToolRegistry) CoreDefinitions() []ToolDef {
	if r == nil {
		return nil
	}
	var out []ToolDef
	for _, name := range r.order {
		tool := r.tools[name]
		if tool.Core {
			out = append(out, tool.Definition())
		}
	}
	return out
}

func (r *ToolRegistry) Definitions() []ToolDef {
	tools := r.Tools()
	out := make([]ToolDef, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Definition())
	}
	return out
}

func (r *ToolRegistry) AnthropicDefinitions() []anthropicTool {
	tools := r.Tools()
	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.AnthropicDefinition())
	}
	return out
}

func (r *ToolRegistry) OpenAIDefinitions() []openAITool {
	tools := r.Tools()
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.OpenAIDefinition())
	}
	return out
}

func toolFromFunc(name, description string, parameters map[string]any, fn func(context.Context, json.RawMessage) (string, error)) Tool {
	return Tool{
		Name:        name,
		Description: description,
		Parameters:  parameters,
		Func:        toolFuncAdapter(fn),
	}
}

func jsonSchemaObject(properties map[string]any, required []string, additionalProperties bool) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": additionalProperties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func cloneSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneSchemaValue(v)
	}
	return out
}

func cloneSchemaValue(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		return cloneSchemaMap(vv)
	case []any:
		out := make([]any, len(vv))
		for i, item := range vv {
			out[i] = cloneSchemaValue(item)
		}
		return out
	case []string:
		out := make([]any, len(vv))
		for i, item := range vv {
			out[i] = item
		}
		return out
	default:
		return v
	}
}

func jsonString(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
