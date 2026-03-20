package pi

import (
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello world", "'hello world'"},
		{"it's", "'it'\\''s'"},
		{"", ""},
		{"foo\tbar", "'foo\tbar'"},
		{"$HOME", "'$HOME'"},
	}
	for _, tc := range tests {
		got := shellQuote(tc.input)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFaseCLICommand(t *testing.T) {
	tests := []struct {
		name       string
		bin        string
		config     string
		workID     string
		wantPrefix string
	}{
		{"simple", "fase", "", "w123", "fase work show w123"},
		{"with config", "fase", "/cfg.toml", "w123", "fase --config /cfg.toml work show w123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FaseCLICommand(tc.bin, tc.config, tc.workID)
			if got != tc.wantPrefix {
				t.Errorf("got %q, want %q", got, tc.wantPrefix)
			}
		})
	}
}

func TestFaseCLINoteAdd(t *testing.T) {
	got := FaseCLINoteAdd("fase", "/cfg.toml", "w123", "test note")
	want := "fase --config /cfg.toml work note-add w123 --body 'test note'"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFaseCLIWorkUpdate(t *testing.T) {
	got := FaseCLIWorkUpdate("fase", "/cfg.toml", "w123", "status update")
	want := "fase --config /cfg.toml work update w123 --message 'status update'"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorkEventFormat(t *testing.T) {
	ev := WorkEvent{
		Kind:      "work_updated",
		WorkID:    "w123",
		Title:     "Fix bug",
		State:     "in_progress",
		PrevState: "claimed",
	}
	tb := &ToolBridge{
		faseBin:    "fase",
		configPath: "/cfg.toml",
	}
	msg := tb.formatEvent(ev)
	if msg == "" {
		t.Fatal("expected non-empty message")
	}
	t.Logf("formatted event:\n%s", msg)
}

func TestDeliveryMode(t *testing.T) {
	if DeliverySteer != DeliveryMode("steer") {
		t.Errorf("DeliverySteer = %q, want %q", DeliverySteer, "steer")
	}
	if DeliveryFollowUp != DeliveryMode("follow_up") {
		t.Errorf("DeliveryFollowUp = %q, want %q", DeliveryFollowUp, "follow_up")
	}
}
