package cli

import (
	"testing"
)

func TestAgenticSupervisorPauseResume(t *testing.T) {
	sup := &agenticSupervisor{hostCh: make(chan string, 16)}

	if sup.isPaused() {
		t.Fatal("should not be paused initially")
	}
	sup.pause()
	if !sup.isPaused() {
		t.Fatal("should be paused after pause()")
	}
	sup.resume()
	if sup.isPaused() {
		t.Fatal("should not be paused after resume()")
	}
}

func TestSupervisorSend(t *testing.T) {
	sup := &agenticSupervisor{hostCh: make(chan string, 16)}
	sup.send("hello")

	select {
	case msg := <-sup.hostCh:
		if msg != "hello" {
			t.Fatalf("got %q, want %q", msg, "hello")
		}
	default:
		t.Fatal("expected message on hostCh")
	}
}
