package pi

import (
	"context"
	"fmt"
	"strings"
)

type WorkEvent struct {
	Kind      string
	WorkID    string
	Title     string
	State     string
	PrevState string
}

type ToolBridge struct {
	t            *transport
	faseBin      string
	configPath   string
	eventCh      <-chan WorkEvent
	deliveryMode DeliveryMode
}

func NewToolBridge(t *transport, faseBin, configPath string, eventCh <-chan WorkEvent, mode DeliveryMode) *ToolBridge {
	return &ToolBridge{
		t:            t,
		faseBin:      faseBin,
		configPath:   configPath,
		eventCh:      eventCh,
		deliveryMode: mode,
	}
}

func (tb *ToolBridge) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-tb.eventCh:
			if !ok {
				return
			}
			tb.handleEvent(ctx, ev)
		}
	}
}

func (tb *ToolBridge) handleEvent(ctx context.Context, ev WorkEvent) {
	msg := tb.formatEvent(ev)
	if msg == "" {
		return
	}

	switch tb.deliveryMode {
	case DeliveryFollowUp:
		_ = tb.t.followUp(ctx, msg)
	default:
		_ = tb.t.steer(ctx, msg)
	}
}

func (tb *ToolBridge) formatEvent(ev WorkEvent) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Work %s event:", ev.Kind))
	parts = append(parts, fmt.Sprintf("  work_id: %s", ev.WorkID))
	if ev.Title != "" {
		parts = append(parts, fmt.Sprintf("  title: %s", ev.Title))
	}
	parts = append(parts, fmt.Sprintf("  state: %s", ev.State))
	if ev.PrevState != "" && ev.PrevState != ev.State {
		parts = append(parts, fmt.Sprintf("  prev_state: %s", ev.PrevState))
	}

	parts = append(parts, fmt.Sprintf("To inspect: %s work show %s", tb.faseCmd(), ev.WorkID))

	body := strings.Join(parts, "\n")
	return fmt.Sprintf("[fase:message from=\"work-graph\" type=\"info\"]\n%s\n[/fase:message]", body)
}

func (tb *ToolBridge) faseCmd() string {
	cmd := tb.faseBin
	if tb.configPath != "" {
		cmd += " --config " + tb.configPath
	}
	return cmd
}

func FaseCLICommand(faseBin, configPath string, workID string) string {
	cmd := faseBin
	if configPath != "" {
		cmd += " --config " + configPath
	}
	return fmt.Sprintf("%s work show %s", cmd, workID)
}

func FaseCLINoteAdd(faseBin, configPath, workID, body string) string {
	cmd := faseBin
	if configPath != "" {
		cmd += " --config " + configPath
	}
	return fmt.Sprintf("%s work note-add %s --body %s", cmd, workID, shellQuote(body))
}

func FaseCLIWorkUpdate(faseBin, configPath, workID, message string) string {
	cmd := faseBin
	if configPath != "" {
		cmd += " --config " + configPath
	}
	return fmt.Sprintf("%s work update %s --message %s", cmd, workID, shellQuote(message))
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t\n\"'`$\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
