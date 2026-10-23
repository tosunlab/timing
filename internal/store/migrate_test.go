package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationTwoKeepsVersionOneRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timing.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		migrations[0],
		`PRAGMA user_version = 1`,
		`INSERT INTO sessions (id, project, agent, task, started_at) VALUES ('s_old00001', 'proj', 'a', 'old task', '2026-10-01T09:00:00Z')`,
		`INSERT INTO token_usage (session_id, model, input, output, recorded_at) VALUES ('s_old00001', 'm', 10, 5, '2026-10-01T09:01:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.Get("s_old00001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Task != "old task" || got.ExternalID != "" || got.Usage["m"] != (Usage{Input: 10, Output: 5}) {
		t.Errorf("v1 session after upgrade: %+v", got)
	}
	var nulls int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM token_usage WHERE source_id IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Errorf("v1 usage rows with NULL source_id = %d, want 1", nulls)
	}

	at := time.Date(2026, 10, 1, 9, 5, 0, 0, time.UTC)
	n, err := s.RecordUsageBatch("s_old00001", []UsageRecord{{Model: "m", Usage: Usage{Input: 1}, At: at, SourceID: "msg_1"}})
	if err != nil || n != 1 {
		t.Fatalf("batch on upgraded database: inserted %d, err %v", n, err)
	}
}
