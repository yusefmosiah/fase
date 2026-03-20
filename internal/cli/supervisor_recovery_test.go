package cli

import (
	"testing"
	"time"
)

func TestGitCommitsSinceNonexistentPath(t *testing.T) {
	commits := gitCommitsSince("nonexistent-work", time.Now(), "/nonexistent/path")
	if len(commits) != 0 {
		t.Fatalf("expected 0 commits for nonexistent path, got %d", len(commits))
	}
}

func TestRecoveryEngineCreation(t *testing.T) {
	engine := newRecoveryEngine()
	if engine == nil {
		t.Fatal("newRecoveryEngine should not return nil")
	}
}
