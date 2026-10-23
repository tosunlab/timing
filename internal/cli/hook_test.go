package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type hookSession struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id"`
	Project    string `json:"project"`
	Agent      string `json:"agent"`
	Task       string `json:"task"`
	Open       bool   `json:"open"`
	Seconds    int64  `json:"seconds"`
	Models     map[string]struct {
		Input, Output int64
		CacheRead     int64 `json:"cache_read"`
		CacheWrite    int64 `json:"cache_write"`
	} `json:"models"`
}

func (h *harness) sessions() []hookSession {
	h.t.Helper()
	var out []hookSession
	if err := json.Unmarshal([]byte(h.ok("log", "--all", "--json")), &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) hook(event, sessionID, transcript, cwd, prompt string) result {
	h.t.Helper()
	payload, _ := json.Marshal(map[string]string{
		"hook_event_name": event, "session_id": sessionID, "transcript_path": transcript, "cwd": cwd, "prompt": prompt,
	})
	h.stdin = string(payload)
	return h.run("hook")
}

func (h *harness) hookOK(event, sessionID, transcript, cwd, prompt string) {
	h.t.Helper()
	if r := h.hook(event, sessionID, transcript, cwd, prompt); r.code != 0 || r.err != "" || r.stdout != "" {
		h.t.Fatalf("hook %s: exit %d, stdout %q, stderr %q", event, r.code, r.stdout, r.err)
	}
}

func copyFixture(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "claude", "testdata", from))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHookLifecycle(t *testing.T) {
	h := newHarness(t, "s_00000001", "s_00000002")
	dir := t.TempDir()
	repo := filepath.Join(dir, "myrepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "claude", "abc.jsonl")
	copyFixture(t, "main.jsonl", transcript)
	copyFixture(t, "subagent.jsonl", filepath.Join(dir, "claude", "abc", "subagents", "agent-1.jsonl"))

	h.hookOK("SessionStart", "abc", transcript, repo, "")
	h.advance(time.Minute)
	h.hookOK("UserPromptSubmit", "abc", transcript, repo, "  fix the login bug\nand then more")
	h.hookOK("UserPromptSubmit", "abc", transcript, repo, "a later prompt")
	h.advance(time.Minute)
	h.hookOK("Stop", "abc", transcript, repo, "")
	h.hookOK("Stop", "abc", transcript, repo, "")

	ss := h.sessions()
	if len(ss) != 1 {
		t.Fatalf("got %d sessions, want 1: %+v", len(ss), ss)
	}
	s := ss[0]
	if s.ID != "s_00000001" || s.ExternalID != "abc" || s.Project != "myrepo" || s.Agent != "claude-code" || s.Task != "fix the login bug" || !s.Open {
		t.Errorf("session after Stop = %+v", s)
	}
	fable, haiku := s.Models["claude-fable-5-1"], s.Models["claude-haiku-5"]
	if fable.Input != 5 || fable.Output != 150 || fable.CacheRead != 2500 || fable.CacheWrite != 500 {
		t.Errorf("fable usage = %+v, want each message counted once", fable)
	}
	if haiku.Input != 7 || haiku.Output != 20 || haiku.CacheRead != 300 || haiku.CacheWrite != 40 {
		t.Errorf("subagent usage = %+v", haiku)
	}

	lagged := `{"type":"assistant","uuid":"x","timestamp":"2026-10-23T09:05:00.000Z","message":{"id":"msg_LATE","model":"claude-fable-5-1","usage":{"input_tokens":1,"output_tokens":1000}}}` + "\n"
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("\n" + lagged)
	f.Close()

	h.advance(time.Minute)
	h.hookOK("SessionEnd", "abc", transcript, repo, "")
	h.hookOK("SessionEnd", "abc", transcript, repo, "")

	ss = h.sessions()
	if len(ss) != 1 || ss[0].Open || ss[0].Seconds != 180 {
		t.Fatalf("after SessionEnd = %+v, want one closed session of 180s", ss)
	}
	if got := ss[0].Models["claude-fable-5-1"]; got.Output != 1150 {
		t.Errorf("output = %d, want 1150 including the message that lagged behind Stop", got.Output)
	}

	h.advance(time.Minute)
	h.hookOK("SessionStart", "abc", transcript, repo, "")
	ss = h.sessions()
	if len(ss) != 2 || ss[1].ID != "s_00000002" || ss[1].ExternalID != "abc" || !ss[1].Open || ss[0].Open {
		t.Errorf("resume should open a second segment: %+v", ss)
	}
}

func TestHookProjectComesFromPayloadCwd(t *testing.T) {
	h := newHarness(t, "s_00000001", "s_00000002")
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	h.hookOK("SessionStart", "one", "", other, "")
	h.env["TIMING_PROJECT"] = "pinned"
	h.hookOK("SessionStart", "two", "", other, "")

	ss := h.sessions()
	if len(ss) != 2 || ss[0].Project != "elsewhere" || ss[1].Project != "pinned" {
		t.Errorf("projects = %+v, want payload cwd then TIMING_PROJECT", ss)
	}
}

func TestHookIgnoresOtherSessionsAndUnknownEvents(t *testing.T) {
	h := newHarness(t, "s_00000001", "s_00000002")
	h.ok("start", "--project", "p")
	h.hookOK("PreToolUse", "abc", "", h.cwd, "")
	h.hookOK("UserPromptSubmit", "abc", "", h.cwd, "/compact")
	ss := h.sessions()
	if len(ss) != 2 || ss[0].ExternalID != "" || ss[0].Task != "" || ss[1].ExternalID != "abc" || ss[1].Task != "" {
		t.Errorf("sessions = %+v, want the manual one untouched and one hook session without a task", ss)
	}
}

func TestHookTaskIsCutToEightyRunes(t *testing.T) {
	h := newHarness(t, "s_00000001")
	h.hookOK("UserPromptSubmit", "abc", "", h.cwd, strings.Repeat("é", 100))
	if got := h.sessions()[0].Task; got != strings.Repeat("é", 80) {
		t.Errorf("task has %d runes, want 80", len([]rune(got)))
	}
}

func TestHookNeverExitsTwo(t *testing.T) {
	h := newHarness(t, "s_00000001", "s_00000002", "s_00000003", "s_00000004")
	for name, run := range map[string]func() result{
		"invalid json": func() result { h.stdin = "{"; return h.run("hook") },
		"empty stdin":  func() result { h.stdin = ""; return h.run("hook") },
		"stray argument": func() result {
			h.stdin = `{"session_id":"a","hook_event_name":"PreToolUse"}`
			return h.run("hook", "bogus")
		},
		"stop no transcript": func() result { return h.hook("Stop", "a", "", h.cwd, "") },
		"transcript missing": func() result { return h.hook("Stop", "b", filepath.Join(h.cwd, "none.jsonl"), h.cwd, "") },
	} {
		r := run()
		want := 1
		if name == "stray argument" {
			want = 0
		}
		if r.code != want {
			t.Errorf("%s: exit %d (stderr %q), want %d", name, r.code, r.err, want)
		}
	}
}

func TestHookSessionEndClosesEvenWhenTranscriptIsMissing(t *testing.T) {
	h := newHarness(t, "s_00000001")
	h.hookOK("SessionStart", "abc", "", h.cwd, "")
	if r := h.hook("SessionEnd", "abc", filepath.Join(h.cwd, "none.jsonl"), h.cwd, ""); r.code != 1 {
		t.Errorf("exit %d, want 1", r.code)
	}
	if ss := h.sessions(); len(ss) != 1 || ss[0].Open {
		t.Errorf("sessions = %+v, want one closed", ss)
	}
}

func TestHookInstall(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	h.env["HOME"] = home

	out := h.ok("hook", "install")
	assertEq(t, "first install", out, "added SessionStart\nadded UserPromptSubmit\nadded Stop\nadded SessionEnd\n")
	path := filepath.Join(home, ".claude", "settings.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	assertEq(t, "second install", h.ok("hook", "install"), "already installed\n")
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("settings changed on second install")
	}

	custom := filepath.Join(t.TempDir(), "project", "settings.json")
	out = h.ok("hook", "install", "--settings", custom)
	if !strings.HasPrefix(out, "added SessionStart") {
		t.Errorf("--settings install printed %q", out)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Error(err)
	}

	h.env["HOME"] = ""
	h.expect(2, "HOME is not set", "hook", "install")
}
