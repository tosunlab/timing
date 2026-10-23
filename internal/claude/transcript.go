package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tosunlab/timing/internal/store"
)

type entry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// Usage returns one record per distinct message id, taken from its first assistant entry.
// Claude Code writes one API response as several entries that repeat the same usage, and the
// transcript may end in a half-written line, so entries that do not parse are skipped.
func Usage(path string) ([]store.UsageRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var recs []store.UsageRecord
	seen := map[string]bool{}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if rec, ok := record(line); ok && !seen[rec.SourceID] {
			seen[rec.SourceID] = true
			recs = append(recs, rec)
		}
		if errors.Is(err, io.EOF) {
			return recs, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func record(line []byte) (store.UsageRecord, bool) {
	var e entry
	if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Message.ID == "" || e.Message.Usage == nil {
		return store.UsageRecord{}, false
	}
	at, err := time.Parse(time.RFC3339, e.Timestamp)
	u := store.Usage{
		Input:      e.Message.Usage.Input,
		Output:     e.Message.Usage.Output,
		CacheRead:  e.Message.Usage.CacheRead,
		CacheWrite: e.Message.Usage.CacheWrite,
	}
	if err != nil || u.Input < 0 || u.Output < 0 || u.CacheRead < 0 || u.CacheWrite < 0 {
		return store.UsageRecord{}, false
	}
	return store.UsageRecord{Model: e.Message.Model, Usage: u, At: at, SourceID: e.Message.ID}, true
}

// SubagentDir is where Claude Code writes the transcripts of a session's subagents.
func SubagentDir(transcriptPath, sessionID string) string {
	return filepath.Join(filepath.Dir(transcriptPath), sessionID, "subagents")
}
