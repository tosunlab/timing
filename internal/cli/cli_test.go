package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tosunlab/timing/internal/cli"
	"github.com/tosunlab/timing/internal/store"
)

type harness struct {
	t      *testing.T
	env    map[string]string
	cwd    string
	now    time.Time
	ids    []store.SessionID
	stdin  string
	nextID int
}

func newHarness(t *testing.T, ids ...store.SessionID) *harness {
	t.Helper()
	dir := t.TempDir()
	return &harness{
		t:   t,
		env: map[string]string{"TIMING_DB": filepath.Join(dir, "timing.db")},
		cwd: dir,
		now: time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC),
		ids: ids,
	}
}

type result struct {
	code        int
	stdout, err string
}

func (h *harness) run(args ...string) result {
	h.t.Helper()
	var out, errb bytes.Buffer
	code := cli.Run(args, cli.IO{
		Stdin:  strings.NewReader(h.stdin),
		Stdout: &out,
		Stderr: &errb,
		Env:    func(k string) string { return h.env[k] },
		Cwd:    h.cwd,
		Now:    func() time.Time { return h.now },
		NewID: func() store.SessionID {
			id := h.ids[h.nextID]
			h.nextID++
			return id
		},
	})
	return result{code, out.String(), errb.String()}
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *harness) ok(args ...string) string {
	h.t.Helper()
	r := h.run(args...)
	if r.code != 0 || r.err != "" {
		h.t.Fatalf("timing %v: exit %d, stderr %q", args, r.code, r.err)
	}
	return r.stdout
}

func (h *harness) expect(code int, stderrHas string, args ...string) result {
	h.t.Helper()
	r := h.run(args...)
	if r.code != code || !strings.Contains(r.err, stderrHas) {
		h.t.Fatalf("timing %v: exit %d (want %d), stderr %q (want substring %q)", args, r.code, code, r.err, stderrHas)
	}
	return r
}

func assertEq(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got: %q\nwant: %q", name, got, want)
	}
}

func TestUsageBlock(t *testing.T) {
	h := newHarness(t, "s_3f9a1c2e")

	assertEq(t, "start", h.ok("start", "--project", "timing", "--agent", "claude-code", "--task", "fix auth bug"), "s_3f9a1c2e\n")

	h.advance(5 * time.Minute)
	assertEq(t, "tokens", h.ok("tokens", "--session", "s_3f9a1c2e", "--model", "claude-fable-5-1",
		"--input", "12000", "--output", "800", "--cache-read", "40000"), "")

	h.advance(7*time.Minute + 3*time.Second)
	assertEq(t, "stop", h.ok("stop", "--session", "s_3f9a1c2e", "--model", "claude-fable-5-1", "--input", "3000", "--output", "200"), "")

	assertEq(t, "amend", h.ok("amend", "s_3f9a", "--task", "fix auth token refresh"),
		"id       s_3f9a1c2e\n"+
			"project  timing\n"+
			"agent    claude-code\n"+
			"task     fix auth token refresh\n"+
			"start    2026-10-23T09:00:00Z\n"+
			"end      2026-10-23T09:12:03Z\n"+
			"time     12m 03s\n"+
			"tokens   input 15000, output 1000, cache-read 40000, cache-write 0\n")

	assertEq(t, "status", h.ok("status", "--project", "timing"), "no open sessions\n")

	assertEq(t, "report", h.ok("report", "--project", "timing"),
		"agent        sessions  time     input  output  cache-read  cache-write\n"+
			"claude-code  1         12m 03s  15000  1000    40000       0\n"+
			"total        1         12m 03s  15000  1000    40000       0\n")

	assertEq(t, "report json", h.ok("report", "--all", "--by", "project", "--json"),
		`[{"key":"timing","sessions":1,"seconds":723,"tokens":{"input":15000,"output":1000,"cache_read":40000,"cache_write":0}}]`+"\n")

	assertEq(t, "log json", h.ok("log", "--project", "timing", "--json"),
		`[{"id":"s_3f9a1c2e","project":"timing","agent":"claude-code","task":"fix auth token refresh",`+
			`"start":"2026-10-23T09:00:00Z","end":"2026-10-23T09:12:03Z","open":false,"seconds":723,`+
			`"tokens":{"input":15000,"output":1000,"cache_read":40000,"cache_write":0},`+
			`"models":{"claude-fable-5-1":{"input":15000,"output":1000,"cache_read":40000,"cache_write":0}}}]`+"\n")
}

func TestAmbiguousSessionListsOpenOnes(t *testing.T) {
	h := newHarness(t, "s_aaaa0001", "s_bbbb0002")
	h.ok("start", "--project", "p", "--agent", "one", "--task", "first")
	h.ok("start", "--project", "p", "--agent", "two", "--task", "second")

	r := h.expect(2, "2 open sessions", "tokens", "--project", "p", "--input", "1")
	for _, want := range []string{"s_aaaa0001", "s_bbbb0002", "first", "second"} {
		if !strings.Contains(r.err, want) {
			t.Errorf("stderr missing %q: %s", want, r.err)
		}
	}
	h.expect(2, "2 open sessions", "stop", "--project", "p")
	if strings.Contains(h.ok("status", "--project", "p", "--json"), `"input":1`) {
		t.Error("ambiguous call recorded usage")
	}

	h.env["TIMING_SESSION"] = "s_bbbb"
	h.ok("tokens", "--project", "p", "--input", "1")
	if out := h.ok("log", "--project", "p", "--json"); !strings.Contains(out, `"id":"s_bbbb0002"`) || !strings.Contains(out, `"tokens":{"input":1`) {
		t.Errorf("TIMING_SESSION not honored: %s", out)
	}
}

func TestSingleOpenSessionIsImplicit(t *testing.T) {
	h := newHarness(t, "s_aaaa0001")
	h.ok("start", "--project", "p")
	h.ok("tokens", "--project", "p", "--input", "7")
	h.advance(time.Minute)
	h.ok("stop", "--project", "p")
	out := h.ok("report", "--project", "p", "--json")
	if !strings.Contains(out, `"input":7`) || !strings.Contains(out, `"seconds":60`) {
		t.Errorf("report: %s", out)
	}
	h.expect(2, "no open sessions", "tokens", "--project", "p", "--input", "1")
}

func TestStopIsIdempotent(t *testing.T) {
	h := newHarness(t, "s_aaaa0001")
	h.ok("start", "--project", "p")
	h.advance(time.Minute)
	h.ok("stop", "--session", "s_aaaa")
	h.advance(time.Hour)
	r := h.run("stop", "--session", "s_aaaa", "--input", "50")
	if r.code != 0 || r.stdout != "" || r.err != "already stopped at 2026-10-23T09:01:00Z\n" {
		t.Errorf("second stop: %+v", r)
	}
	if out := h.ok("log", "--project", "p", "--json"); !strings.Contains(out, `"input":0`) || !strings.Contains(out, `"seconds":60`) {
		t.Errorf("second stop changed the session: %s", out)
	}
}

func TestTokensInputRules(t *testing.T) {
	h := newHarness(t, "s_aaaa0001")
	h.ok("start", "--project", "p")

	h.expect(2, "at least one token count", "tokens", "--project", "p")
	h.expect(2, "non-zero", "tokens", "--project", "p", "--input", "0")
	h.expect(2, "must not be negative", "tokens", "--project", "p", "--input", "-5")
	h.expect(2, "needs at least one token count", "tokens", "--project", "p", "--model", "m")
	h.expect(2, "", "tokens", "--project", "p", "--input", "abc")
	h.expect(2, "unknown flag", "tokens", "--project", "p", "--bogus")

	h.stdin = `{"model":"m1","input":10,"output":2,"cache_read":3,"cache_write":4}`
	h.ok("tokens", "--project", "p", "--stdin-json")
	h.stdin = `{"input_tokens":10}`
	h.expect(2, "invalid usage JSON", "tokens", "--project", "p", "--stdin-json")
	h.stdin = `{"input":1}`
	h.expect(2, "cannot be combined", "tokens", "--project", "p", "--stdin-json", "--input", "1")
	h.stdin = `{}`
	h.expect(2, "non-zero", "tokens", "--project", "p", "--stdin-json")

	out := h.ok("log", "--project", "p", "--json")
	if !strings.Contains(out, `"models":{"m1":{"input":10,"output":2,"cache_read":3,"cache_write":4}}`) {
		t.Errorf("stdin usage not recorded: %s", out)
	}

	h.ok("stop", "--project", "p")
	h.expect(2, "closed", "tokens", "--session", "s_aaaa", "--input", "1")
}

func TestAmend(t *testing.T) {
	h := newHarness(t, "s_aaaa0001", "s_aaab0002")
	h.ok("start", "--project", "p")
	h.ok("start", "--project", "p")

	h.expect(2, "at least one of", "amend", "s_aaaa")
	h.expect(2, "", "amend")
	h.expect(2, "ambiguous", "amend", "s_aaa", "--task", "x")
	h.expect(2, "no session matches", "amend", "s_zzz", "--task", "x")
	h.expect(2, "--project must not be empty", "amend", "s_aaaa", "--project", "")
	h.expect(2, "invalid time", "amend", "s_aaaa", "--end", "yesterday-ish")
	h.expect(2, "before start", "amend", "s_aaaa", "--end", "2026-10-22")

	out := h.ok("amend", "s_aaaa", "--json", "--agent", "codex", "--project", "q", "--end", "2026-10-23 09:30")
	if !strings.Contains(out, `"agent":"codex"`) || !strings.Contains(out, `"project":"q"`) ||
		!strings.Contains(out, `"open":false`) || !strings.Contains(out, `"end":"2026-10-23T09:30:00Z"`) {
		t.Errorf("amended session: %s", out)
	}
	h.ok("amend", "s_aaaa", "--start", "-2h", "--end", "-30m")
	if out := h.ok("log", "--project", "q", "--json"); !strings.Contains(out, `"seconds":5400`) {
		t.Errorf("relative times: %s", out)
	}
}

func TestProjectResolution(t *testing.T) {
	h := newHarness(t, "s_aaaa0001", "s_aaaa0002", "s_aaaa0003", "s_aaaa0004")
	repo := filepath.Join(t.TempDir(), "myrepo")
	sub := filepath.Join(repo, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	projectOf := func(id string) string {
		out := h.ok("log", "--all", "--json")
		_, rest, _ := strings.Cut(out, `"id":"`+id+`","project":"`)
		p, _, _ := strings.Cut(rest, `"`)
		return p
	}

	h.cwd = sub
	h.ok("start")
	assertEq(t, "git toplevel basename", projectOf("s_aaaa0001"), "myrepo")

	h.env["TIMING_PROJECT"] = "from-env"
	h.ok("start")
	assertEq(t, "env beats git", projectOf("s_aaaa0002"), "from-env")

	h.ok("start", "--project", "from-flag")
	assertEq(t, "flag beats env", projectOf("s_aaaa0003"), "from-flag")

	delete(h.env, "TIMING_PROJECT")
	h.cwd = t.TempDir()
	h.ok("start")
	assertEq(t, "cwd basename", projectOf("s_aaaa0004"), filepath.Base(h.cwd))
}

func TestScopeAndFilters(t *testing.T) {
	h := newHarness(t, "s_a0000001", "s_b0000001", "s_a0000002")
	h.ok("start", "--project", "a", "--agent", "x", "--task", "t1")
	h.advance(24 * time.Hour)
	h.ok("start", "--project", "b", "--agent", "y")
	h.advance(24 * time.Hour)
	h.ok("start", "--project", "a", "--agent", "x", "--task", "t2")

	ids := func(args ...string) string {
		out := h.ok(args...)
		var found []string
		for _, id := range []string{"s_a0000001", "s_b0000001", "s_a0000002"} {
			if strings.Contains(out, id) {
				found = append(found, id)
			}
		}
		return strings.Join(found, ",")
	}
	assertEq(t, "log scoped", ids("log", "--project", "a"), "s_a0000001,s_a0000002")
	assertEq(t, "log all", ids("log", "--all"), "s_a0000001,s_b0000001,s_a0000002")
	assertEq(t, "log since date", ids("log", "--all", "--since", "2026-10-24"), "s_b0000001,s_a0000002")
	assertEq(t, "log since relative", ids("log", "--all", "--since", "-1h"), "s_a0000002")
	assertEq(t, "log limit", ids("log", "--all", "--limit", "2"), "s_b0000001,s_a0000002")
	assertEq(t, "status scoped", ids("status", "--project", "b"), "s_b0000001")
	h.expect(2, "invalid time", "log", "--since", "nope")

	status := h.ok("status", "--all")
	if !strings.HasPrefix(status, "id          project  agent  task  start") || !strings.Contains(status, "48h 00m 00s") {
		t.Errorf("status --all:\n%s", status)
	}
	log := h.ok("log", "--project", "a")
	if !strings.Contains(log, "open") || !strings.HasPrefix(log, "id          agent  task  start") {
		t.Errorf("log:\n%s", log)
	}
}

func TestReportGroupings(t *testing.T) {
	h := newHarness(t, "s_aaaa0001", "s_bbbb0002")
	h.ok("start", "--project", "p", "--agent", "x", "--task", "alpha")
	h.ok("tokens", "--session", "s_aaaa", "--model", "m1", "--input", "10")
	h.ok("tokens", "--session", "s_aaaa", "--model", "m2", "--output", "20")
	h.advance(time.Hour)
	h.ok("stop", "--session", "s_aaaa")
	h.advance(24 * time.Hour)
	h.ok("start", "--project", "p", "--agent", "x")
	h.advance(90 * time.Second)

	assertEq(t, "by model", h.ok("report", "--project", "p", "--by", "model"),
		"model   sessions  time        input  output  cache-read  cache-write\n"+
			"(none)  1         1m 30s      0      0       0           0\n"+
			"m1      1         1h 00m 00s  10     0       0           0\n"+
			"m2      1         1h 00m 00s  0      20      0           0\n"+
			"total   2         1h 01m 30s  10     20      0           0\n")
	assertEq(t, "by day", h.ok("report", "--project", "p", "--by", "day", "--json"),
		`[{"key":"2026-10-23","sessions":1,"seconds":3600,"tokens":{"input":10,"output":20,"cache_read":0,"cache_write":0}},`+
			`{"key":"2026-10-24","sessions":1,"seconds":90,"tokens":{"input":0,"output":0,"cache_read":0,"cache_write":0}}]`+"\n")
	assertEq(t, "by task", h.ok("report", "--project", "p", "--by", "task", "--json"),
		`[{"key":"(none)","sessions":1,"seconds":90,"tokens":{"input":0,"output":0,"cache_read":0,"cache_write":0}},`+
			`{"key":"alpha","sessions":1,"seconds":3600,"tokens":{"input":10,"output":20,"cache_read":0,"cache_write":0}}]`+"\n")
	h.expect(2, "--by must be one of", "report", "--by", "color")
	assertEq(t, "empty report json", h.ok("report", "--project", "nothing", "--json"), "[]\n")
	assertEq(t, "empty log json", h.ok("log", "--project", "nothing", "--json"), "[]\n")
	assertEq(t, "empty log", h.ok("log", "--project", "nothing"), "no sessions\n")
}

func TestStartDefaultsAndJSON(t *testing.T) {
	h := newHarness(t, "s_aaaa0001", "s_aaaa0002")
	h.env["TIMING_AGENT"] = "codex"
	out := h.ok("start", "--project", "p", "--json")
	assertEq(t, "start json", out,
		`{"id":"s_aaaa0001","project":"p","agent":"codex","task":"","start":"2026-10-23T09:00:00Z","end":null,"open":true,"seconds":0,`+
			`"tokens":{"input":0,"output":0,"cache_read":0,"cache_write":0},"models":{}}`+"\n")
	delete(h.env, "TIMING_AGENT")
	h.ok("start", "--project", "p")
	if out := h.ok("log", "--project", "p", "--json"); !strings.Contains(out, `"agent":"unknown"`) {
		t.Errorf("agent default: %s", out)
	}
}

func TestDatabasePathResolution(t *testing.T) {
	h := newHarness(t, "s_aaaa0001", "s_aaaa0002", "s_aaaa0003")
	dir := t.TempDir()
	delete(h.env, "TIMING_DB")

	h.env["XDG_DATA_HOME"] = filepath.Join(dir, "xdg")
	h.ok("start", "--project", "p")
	if _, err := os.Stat(filepath.Join(dir, "xdg", "timing", "timing.db")); err != nil {
		t.Errorf("XDG_DATA_HOME not used: %v", err)
	}

	delete(h.env, "XDG_DATA_HOME")
	h.env["HOME"] = filepath.Join(dir, "home")
	h.ok("start", "--project", "p")
	if _, err := os.Stat(filepath.Join(dir, "home", ".local", "share", "timing", "timing.db")); err != nil {
		t.Errorf("HOME fallback not used: %v", err)
	}

	flagPath := filepath.Join(dir, "flag.db")
	h.env["TIMING_DB"] = filepath.Join(dir, "env.db")
	h.ok("start", "--project", "p", "--db", flagPath)
	if _, err := os.Stat(flagPath); err != nil {
		t.Errorf("--db not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "env.db")); err == nil {
		t.Error("TIMING_DB used despite --db")
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	h := newHarness(t, "s_aaaa0001")
	h.expect(2, "unknown command", "frobnicate")
	h.expect(2, "unknown flag", "status", "--nope")
	h.expect(2, "unknown command", "status", "extra")
	if r := h.run(); r.code != 0 || !strings.Contains(r.stdout, "Available Commands") {
		t.Errorf("bare invocation: %+v", r)
	}
	if r := h.run("--help"); r.code != 0 {
		t.Errorf("--help exit %d", r.code)
	}
}

func TestRuntimeErrorExitsOne(t *testing.T) {
	h := newHarness(t, "s_aaaa0001")
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.expect(1, "timing:", "status", "--db", filepath.Join(blocker, "sub", "x.db"))
}

func TestDurationFormatting(t *testing.T) {
	h := newHarness(t, "s_aaaa0001")
	h.ok("start", "--project", "p")
	for d, want := range map[time.Duration]string{
		0:                               "0s",
		45 * time.Second:                "45s",
		59*time.Minute + 59*time.Second: "59m 59s",
		time.Hour + 2*time.Minute + 3*time.Second: "1h 02m 03s",
	} {
		h.now = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC).Add(d)
		out := h.ok("status", "--project", "p")
		if !strings.Contains(out, "  "+want+"  ") {
			t.Errorf("elapsed %v: want %q in\n%s", d, want, out)
		}
	}
}
