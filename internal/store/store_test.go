package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tosunlab/timing/internal/store"
)

var t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

func open(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "timing.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func start(t *testing.T, s *store.Store, id, project string, at time.Time) store.SessionID {
	t.Helper()
	sid := store.SessionID(id)
	if err := s.Start(store.NewSession{ID: sid, Project: project, Agent: "a", Task: "t", Start: at}); err != nil {
		t.Fatal(err)
	}
	return sid
}

func ptr[T any](v T) *T { return &v }

func TestRoundTripAndPersistence(t *testing.T) {
	s, path := open(t)
	id := start(t, s, "s_aaaa0001", "proj", t0)
	if err := s.RecordUsage(id, store.UsageRecord{Model: "m1", Usage: store.Usage{Input: 10, Output: 2}, At: t0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(id, store.UsageRecord{Model: "m1", Usage: store.Usage{Input: 5, CacheRead: 7}, At: t0.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(id, store.UsageRecord{Usage: store.Usage{Output: 1}, At: t0.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Open() || got.Project != "proj" || got.Agent != "a" || got.Task != "t" || !got.Start.Equal(t0) {
		t.Fatalf("unexpected session: %+v", got)
	}
	if want := (store.Usage{Input: 15, Output: 2, CacheRead: 7}); got.Usage["m1"] != want {
		t.Fatalf("m1 usage = %+v, want %+v", got.Usage["m1"], want)
	}
	if want := (store.Usage{Output: 1}); got.Usage[""] != want {
		t.Fatalf("unnamed model usage = %+v, want %+v", got.Usage[""], want)
	}
	if got.TotalUsage().Total() != 15+2+7+1 {
		t.Fatalf("total = %d", got.TotalUsage().Total())
	}
	if d := got.Duration(t0.Add(time.Hour)); d != time.Hour {
		t.Fatalf("open duration = %v", d)
	}

	end := t0.Add(10 * time.Minute)
	if err := s.Stop(id, end, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err = s2.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Open() || !got.End.Equal(end) || got.Duration(t0.Add(99*time.Hour)) != 10*time.Minute {
		t.Fatalf("after reopen: %+v", got)
	}
}

func TestStopWithUsageIsOneStep(t *testing.T) {
	s, _ := open(t)
	id := start(t, s, "s_aaaa0001", "p", t0)
	end := t0.Add(time.Minute)
	if err := s.Stop(id, end, &store.UsageRecord{Model: "m", Usage: store.Usage{Input: 3}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(id)
	if got.Open() || got.Usage["m"].Input != 3 {
		t.Fatalf("got %+v", got)
	}

	err := s.Stop(id, end.Add(time.Hour), &store.UsageRecord{Model: "m", Usage: store.Usage{Input: 100}})
	if !errors.Is(err, store.ErrAlreadyStopped) {
		t.Fatalf("second stop err = %v", err)
	}
	got, _ = s.Get(id)
	if !got.End.Equal(end) || got.Usage["m"].Input != 3 {
		t.Fatalf("second stop changed the session: %+v", got)
	}
}

func TestStopRollsBackUsageWhenEndInvalid(t *testing.T) {
	s, _ := open(t)
	id := start(t, s, "s_aaaa0001", "p", t0)
	err := s.Stop(id, t0.Add(-time.Minute), &store.UsageRecord{Usage: store.Usage{Input: 3}})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	got, _ := s.Get(id)
	if !got.Open() || !got.TotalUsage().IsZero() {
		t.Fatalf("failed stop left state behind: %+v", got)
	}
}

func TestRejections(t *testing.T) {
	s, _ := open(t)
	id := start(t, s, "s_aaaa0001", "p", t0)
	missing := store.SessionID("s_nope")

	if err := s.RecordUsage(missing, store.UsageRecord{At: t0}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("usage on missing session: %v", err)
	}
	if err := s.Stop(missing, t0, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stop missing: %v", err)
	}
	if err := s.Amend(missing, store.Amendment{Task: ptr("x")}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("amend missing: %v", err)
	}
	if _, err := s.Get(missing); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}
	if err := s.RecordUsage(id, store.UsageRecord{Usage: store.Usage{Input: -1}, At: t0}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("negative usage: %v", err)
	}
	if err := s.Start(store.NewSession{ID: id, Project: "p", Agent: "a", Start: t0}); err == nil {
		t.Error("duplicate id accepted")
	}
	if err := s.Stop(id, t0.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(id, store.UsageRecord{Usage: store.Usage{Input: 1}, At: t0}); !errors.Is(err, store.ErrClosed) {
		t.Errorf("usage on closed session: %v", err)
	}
}

func TestResolvePrefix(t *testing.T) {
	s, _ := open(t)
	start(t, s, "s_3f9a1c2e", "p", t0)
	start(t, s, "s_3f9b0000", "p", t0)
	start(t, s, "s_abcd0000", "p", t0)

	cases := []struct {
		prefix string
		want   store.SessionID
		err    error
	}{
		{"s_3f9a", "s_3f9a1c2e", nil},
		{"s_3f9a1c2e", "s_3f9a1c2e", nil},
		{"s_a", "s_abcd0000", nil},
		{"s_3f9", "", store.ErrAmbiguous},
		{"s_", "", store.ErrAmbiguous},
		{"s_zzzz", "", store.ErrNotFound},
		{"sXabcd", "", store.ErrNotFound}, // "_" is not a wildcard
		{"", "", store.ErrNotFound},
	}
	for _, c := range cases {
		got, err := s.Resolve(c.prefix)
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("Resolve(%q) = %q, %v; want %q, %v", c.prefix, got, err, c.want, c.err)
		}
	}
}

func TestAmend(t *testing.T) {
	s, _ := open(t)
	id := start(t, s, "s_aaaa0001", "p", t0)

	if err := s.Amend(id, store.Amendment{Task: ptr("new task"), Agent: ptr("codex"), Project: ptr("q")}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(id)
	if got.Task != "new task" || got.Agent != "codex" || got.Project != "q" || !got.Open() || !got.Start.Equal(t0) {
		t.Fatalf("got %+v", got)
	}

	if err := s.Amend(id, store.Amendment{End: ptr(t0.Add(-time.Second))}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("end before start: %v", err)
	}
	if got, _ := s.Get(id); !got.Open() {
		t.Fatal("rejected amend changed the session")
	}

	end := t0.Add(5 * time.Minute)
	if err := s.Amend(id, store.Amendment{End: &end}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(id); got.Open() || !got.End.Equal(end) {
		t.Fatalf("end on open session did not close it: %+v", got)
	}

	if err := s.Amend(id, store.Amendment{Start: ptr(end.Add(time.Second))}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("start after end: %v", err)
	}
	if err := s.Amend(id, store.Amendment{Start: ptr(t0.Add(-time.Hour))}); err != nil {
		t.Fatal(err)
	}
}

func TestListFilters(t *testing.T) {
	s, _ := open(t)
	a1 := start(t, s, "s_a0000001", "a", t0)
	start(t, s, "s_b0000001", "b", t0.Add(time.Hour))
	a2 := start(t, s, "s_a0000002", "a", t0.Add(2*time.Hour))
	a3 := start(t, s, "s_a0000003", "a", t0.Add(3*time.Hour))
	if err := s.Stop(a1, t0.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(a3, store.UsageRecord{Model: "m", Usage: store.Usage{Input: 9}, At: t0.Add(4 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	ids := func(f store.Filter) []store.SessionID {
		t.Helper()
		got, err := s.List(f)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.SessionID
		for _, x := range got {
			out = append(out, x.ID)
		}
		return out
	}
	eq := func(name string, got []store.SessionID, want ...store.SessionID) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	eq("all", ids(store.Filter{}), a1, "s_b0000001", a2, a3)
	eq("project", ids(store.Filter{Project: "a"}), a1, a2, a3)
	eq("open", ids(store.Filter{Project: "a", OpenOnly: true}), a2, a3)
	eq("since", ids(store.Filter{Since: t0.Add(2 * time.Hour)}), a2, a3)
	eq("limit keeps newest, oldest first", ids(store.Filter{Project: "a", Limit: 2}), a2, a3)

	got, _ := s.List(store.Filter{Project: "a", Limit: 1})
	if len(got) != 1 || got[0].Usage["m"].Input != 9 {
		t.Errorf("usage not loaded for limited list: %+v", got)
	}
}

func TestConcurrentRecordUsage(t *testing.T) {
	_, path := open(t)
	const writers, perWriter = 2, 100

	ids := make([]store.SessionID, writers)
	handles := make([]*store.Store, writers)
	for i := range handles {
		// Separate handles model separate agent processes sharing one file.
		h, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		handles[i] = h
		ids[i] = start(t, h, fmt.Sprintf("s_c000000%d", i), "p", t0)
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for i := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				err := handles[i].RecordUsage(ids[i], store.UsageRecord{Model: "m", Usage: store.Usage{Input: 1, Output: 2}, At: t0})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	for i, id := range ids {
		got, err := handles[0].Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if want := (store.Usage{Input: perWriter, Output: 2 * perWriter}); got.Usage["m"] != want {
			t.Errorf("writer %d usage = %+v, want %+v", i, got.Usage["m"], want)
		}
	}
}

func TestUsageArithmetic(t *testing.T) {
	a := store.Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}
	if got := a.Add(a); got != (store.Usage{Input: 2, Output: 4, CacheRead: 6, CacheWrite: 8}) {
		t.Errorf("Add = %+v", got)
	}
	if a.Total() != 10 || a.IsZero() || !(store.Usage{}).IsZero() {
		t.Error("Total or IsZero wrong")
	}
}

func TestRecordUsageBatchDeduplicatesBySource(t *testing.T) {
	s, _ := open(t)
	id := start(t, s, "s_aaaa0001", "proj", t0)
	other := start(t, s, "s_aaaa0002", "proj", t0)
	rec := func(source string, in int64) store.UsageRecord {
		return store.UsageRecord{Model: "m", Usage: store.Usage{Input: in}, At: t0.Add(time.Minute), SourceID: source}
	}

	n, err := s.RecordUsageBatch(id, []store.UsageRecord{rec("a", 1), rec("b", 2), rec("a", 100)})
	if err != nil || n != 2 {
		t.Fatalf("first batch: inserted %d, err %v; want 2", n, err)
	}
	n, err = s.RecordUsageBatch(id, []store.UsageRecord{rec("a", 1), rec("b", 2), rec("c", 4)})
	if err != nil || n != 1 {
		t.Fatalf("second batch: inserted %d, err %v; want 1", n, err)
	}
	n, err = s.RecordUsageBatch(other, []store.UsageRecord{rec("c", 4)})
	if err != nil || n != 0 {
		t.Fatalf("same source on another session: inserted %d, err %v; want 0", n, err)
	}

	got, _ := s.Get(id)
	if u := got.Usage["m"]; u.Input != 7 {
		t.Errorf("input = %d, want 7", u.Input)
	}

	// Empty SourceID is a manual row and never deduplicated.
	n, err = s.RecordUsageBatch(id, []store.UsageRecord{rec("", 1), rec("", 1)})
	if err != nil || n != 2 {
		t.Fatalf("manual rows: inserted %d, err %v; want 2", n, err)
	}
}

func TestRecordUsageBatchSpansTransactions(t *testing.T) {
	s, _ := open(t)
	id := start(t, s, "s_aaaa0001", "proj", t0)
	var recs []store.UsageRecord
	for i := range 1203 {
		recs = append(recs, store.UsageRecord{Model: "m", Usage: store.Usage{Output: 1}, At: t0, SourceID: fmt.Sprintf("msg_%d", i)})
	}
	n, err := s.RecordUsageBatch(id, recs)
	if err != nil || n != 1203 {
		t.Fatalf("inserted %d, err %v; want 1203", n, err)
	}
	if n, err = s.RecordUsageBatch(id, recs); err != nil || n != 0 {
		t.Fatalf("rerun: inserted %d, err %v; want 0", n, err)
	}
}

func TestRecordUsageBatchRejections(t *testing.T) {
	s, _ := open(t)
	id := start(t, s, "s_aaaa0001", "proj", t0)
	good := store.UsageRecord{Model: "m", Usage: store.Usage{Input: 1}, At: t0, SourceID: "ok"}
	bad := store.UsageRecord{Model: "m", Usage: store.Usage{Input: -1}, At: t0, SourceID: "bad"}

	if _, err := s.RecordUsageBatch(id, []store.UsageRecord{good, bad}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("negative count: err %v, want ErrInvalid", err)
	}
	if got, _ := s.Get(id); !got.TotalUsage().IsZero() {
		t.Errorf("rejected batch left usage %+v", got.TotalUsage())
	}
	if _, err := s.RecordUsageBatch("s_missing0", []store.UsageRecord{good}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown session: err %v, want ErrNotFound", err)
	}
	if err := s.Stop(id, t0.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordUsageBatch(id, []store.UsageRecord{good}); !errors.Is(err, store.ErrClosed) {
		t.Errorf("closed session: err %v, want ErrClosed", err)
	}
}

func TestNewestByExternalID(t *testing.T) {
	s, _ := open(t)
	if _, err := s.Newest("x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty store: err %v, want ErrNotFound", err)
	}
	for i, id := range []string{"s_aaaa0001", "s_aaaa0002", "s_aaaa0003"} {
		ext := "ext-1"
		if i == 2 {
			ext = ""
		}
		if err := s.Start(store.NewSession{ID: store.SessionID(id), ExternalID: ext, Project: "p", Agent: "a", Start: t0.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Newest("ext-1")
	if err != nil || got.ID != "s_aaaa0002" || got.ExternalID != "ext-1" {
		t.Errorf("Newest(ext-1) = %+v, %v; want s_aaaa0002", got, err)
	}
	if _, err := s.Newest(""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Newest(\"\"): err %v, want ErrNotFound", err)
	}
}
