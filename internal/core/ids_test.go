package core

import (
	"strings"
	"testing"
)

func TestGenerateIDUsesPrefix(t *testing.T) {
	id := GenerateID("job")

	if !strings.HasPrefix(id, "job_") {
		t.Fatalf("expected ID to start with job_, got %q", id)
	}
}
