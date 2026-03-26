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

func TestCogentCLICommand(t *testing.T) {
	tests := []struct {
		name       string
		bin        string
		config     string
		workID     string
		wantPrefix string
	}{
		{"simple", "cogent", "", "w123", "cogent work show w123"},
		{"with config", "cogent", "/cfg.toml", "w123", "cogent --config /cfg.toml work show w123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CogentCLICommand(tc.bin, tc.config, tc.workID)
			if got != tc.wantPrefix {
				t.Errorf("got %q, want %q", got, tc.wantPrefix)
			}
		})
	}
}

func TestCogentCLINoteAdd(t *testing.T) {
	got := CogentCLINoteAdd("cogent", "/cfg.toml", "w123", "test note")
	want := "cogent --config /cfg.toml work note-add w123 --body 'test note'"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCogentCLIWorkUpdate(t *testing.T) {
	got := CogentCLIWorkUpdate("cogent", "/cfg.toml", "w123", "status update")
	want := "cogent --config /cfg.toml work update w123 --message 'status update'"
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
		cogentBin:    "cogent",
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
