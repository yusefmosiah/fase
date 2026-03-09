package events

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTranslateFixtures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		adapter string
		fixture string
		golden  string
	}{
		{
			name:    "codex",
			adapter: "codex",
			fixture: filepath.Join("..", "..", "testdata", "fixtures", "codex", "success.jsonl"),
			golden:  filepath.Join("..", "..", "testdata", "golden", "codex", "success.events.json"),
		},
		{
			name:    "claude",
			adapter: "claude",
			fixture: filepath.Join("..", "..", "testdata", "fixtures", "claude", "success.jsonl"),
			golden:  filepath.Join("..", "..", "testdata", "golden", "claude", "success.events.json"),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := os.Open(tc.fixture)
			if err != nil {
				t.Fatalf("open fixture: %v", err)
			}
			defer func() { _ = file.Close() }()

			var translated []Hint
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				translated = append(translated, TranslateLine(tc.adapter, "stdout", scanner.Text())...)
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("scan fixture: %v", err)
			}

			wantBytes, err := os.ReadFile(tc.golden)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}

			var want []Hint
			if err := json.Unmarshal(wantBytes, &want); err != nil {
				t.Fatalf("unmarshal golden: %v", err)
			}

			gotBytes, err := json.MarshalIndent(translated, "", "  ")
			if err != nil {
				t.Fatalf("marshal translated output: %v", err)
			}
			wantNormalized, err := json.MarshalIndent(want, "", "  ")
			if err != nil {
				t.Fatalf("marshal normalized golden: %v", err)
			}

			if string(gotBytes) != string(wantNormalized) {
				t.Fatalf("translation mismatch\nwant:\n%s\n\ngot:\n%s", wantNormalized, gotBytes)
			}
		})
	}
}
