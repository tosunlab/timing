package claude_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tosunlab/timing/internal/claude"
	"github.com/tosunlab/timing/internal/store"
)

func TestUsageKeepsOneRecordPerMessageID(t *testing.T) {
	recs, err := claude.Usage("testdata/main.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// msg_A and msg_B repeat across entries, msg_C has no usage, msg_D is a truncated last line.
	want := []store.UsageRecord{
		{Model: "claude-fable-5-1", Usage: store.Usage{Input: 2, Output: 100, CacheRead: 1000, CacheWrite: 500},
			At: time.Date(2026, 10, 23, 9, 0, 5, 0, time.UTC), SourceID: "msg_A"},
		{Model: "claude-fable-5-1", Usage: store.Usage{Input: 3, Output: 50, CacheRead: 1500},
			At: time.Date(2026, 10, 23, 9, 0, 10, 0, time.UTC), SourceID: "msg_B"},
	}
	if len(recs) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(recs), len(want), recs)
	}
	for i := range want {
		if !recs[i].At.Equal(want[i].At) {
			t.Errorf("record %d at %s, want %s", i, recs[i].At, want[i].At)
		}
		recs[i].At = want[i].At
		if recs[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, recs[i], want[i])
		}
	}
}

func TestUsageKeepsSubagentModel(t *testing.T) {
	recs, err := claude.Usage("testdata/subagent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Model != "claude-haiku-5" || recs[0].Usage.Total() != 367 {
		t.Errorf("got %+v", recs)
	}
}

func TestUsageMissingFile(t *testing.T) {
	if _, err := claude.Usage("testdata/nope.jsonl"); err == nil {
		t.Error("want an error for a missing transcript")
	}
}

func TestSubagentDir(t *testing.T) {
	got := claude.SubagentDir("/home/u/.claude/projects/-p/abc.jsonl", "abc")
	if want := "/home/u/.claude/projects/-p/abc/subagents"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestParse(t *testing.T) {
	p, err := claude.Parse(strings.NewReader(`{"session_id":"abc","transcript_path":"/t.jsonl","cwd":"/w","hook_event_name":"UserPromptSubmit","prompt":"hi","extra":1}`))
	want := claude.Payload{SessionID: "abc", TranscriptPath: "/t.jsonl", Cwd: "/w", Event: claude.UserPromptSubmit, Prompt: "hi"}
	if err != nil || p != want {
		t.Errorf("got %+v, %v; want %+v", p, err, want)
	}
	for _, bad := range []string{``, `{`, `{"hook_event_name":"Stop"}`, `{"session_id":"x"}`} {
		if _, err := claude.Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("Parse(%q): want an error", bad)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	added, err := claude.Install(path)
	if err != nil || len(added) != 4 {
		t.Fatalf("first install: added %v, err %v", added, err)
	}
	first := readFile(t, path)
	added, err = claude.Install(path)
	if err != nil || len(added) != 0 {
		t.Fatalf("second install: added %v, err %v", added, err)
	}
	if second := readFile(t, path); second != first {
		t.Errorf("file changed on second install:\n%s\nvs\n%s", first, second)
	}
	if !strings.Contains(first, `"timeout": 10`) || strings.Count(first, `"command": "timing hook"`) != 4 {
		t.Errorf("unexpected settings:\n%s", first)
	}
}

func TestInstallKeepsExistingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{
  "model": "x",
  "permissions": {"allow": ["Bash(ls)"]},
  "hooks": {
    "Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "notify"}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "timing hook", "timeout": 3}]}]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := claude.Install(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 3 || added[0] != claude.SessionStart || added[1] != claude.UserPromptSubmit || added[2] != claude.Stop {
		t.Errorf("added = %v, want SessionStart, UserPromptSubmit, Stop", added)
	}
	got := readFile(t, path)
	for _, want := range []string{`"model": "x"`, `"Bash(ls)"`, `"command": "notify"`, `"timeout": 3`} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %s:\n%s", want, got)
		}
	}
	if strings.Count(got, `"command": "timing hook"`) != 4 {
		t.Errorf("want 4 timing hook entries (one pre-existing):\n%s", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestInstallRejectsMalformedSettings(t *testing.T) {
	for name, content := range map[string]string{
		"not json":     `{`,
		"not object":   `[]`,
		"hooks scalar": `{"hooks": 1}`,
		"event scalar": `{"hooks": {"Stop": {}}}`,
	} {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := claude.Install(path); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if got := readFile(t, path); got != content {
			t.Errorf("%s: file modified to %q", name, got)
		}
	}
}
