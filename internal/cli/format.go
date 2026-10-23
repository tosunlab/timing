package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tosunlab/timing/internal/store"
)

func formatDuration(d time.Duration) string {
	secs := int64(max(d, 0) / time.Second)
	h, m, s := secs/3600, secs%3600/60, secs%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// Bare dates and "date time" are local to now's location; "-30m" style values are relative to now.
func parseTime(s string, now time.Time) (time.Time, error) {
	if strings.HasPrefix(s, "-") {
		d, err := time.ParseDuration(s)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid time %q: want RFC3339, 2006-01-02, \"2006-01-02 15:04\", or a relative value such as -30m", s)
		}
		return now.Add(d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q: want RFC3339, 2006-01-02, \"2006-01-02 15:04\", or a relative value such as -30m", s)
}

func writeTable(w io.Writer, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.TrimRight(strings.Join(r, "\t"), " "))
	}
	tw.Flush()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

var tokenHeader = []string{"input", "output", "cache-read", "cache-write"}

func tokenCells(u store.Usage) []string {
	return []string{itoa(u.Input), itoa(u.Output), itoa(u.CacheRead), itoa(u.CacheWrite)}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type tokensJSON struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

func toTokensJSON(u store.Usage) tokensJSON {
	return tokensJSON{u.Input, u.Output, u.CacheRead, u.CacheWrite}
}

type sessionJSON struct {
	ID      string                `json:"id"`
	Project string                `json:"project"`
	Agent   string                `json:"agent"`
	Task    string                `json:"task"`
	Start   string                `json:"start"`
	End     *string               `json:"end"`
	Open    bool                  `json:"open"`
	Seconds int64                 `json:"seconds"`
	Tokens  tokensJSON            `json:"tokens"`
	Models  map[string]tokensJSON `json:"models"`
}

func toSessionJSON(s store.Session, now time.Time) sessionJSON {
	j := sessionJSON{
		ID:      string(s.ID),
		Project: s.Project,
		Agent:   s.Agent,
		Task:    s.Task,
		Start:   s.Start.Format(time.RFC3339),
		Open:    s.Open(),
		Seconds: seconds(s.Duration(now)),
		Tokens:  toTokensJSON(s.TotalUsage()),
		Models:  map[string]tokensJSON{},
	}
	if !s.Open() {
		e := s.End.Format(time.RFC3339)
		j.End = &e
	}
	for m, u := range s.Usage {
		j.Models[m] = toTokensJSON(u)
	}
	return j
}

func seconds(d time.Duration) int64 { return int64(max(d, 0) / time.Second) }

func sessionsJSON(ss []store.Session, now time.Time) []sessionJSON {
	out := make([]sessionJSON, 0, len(ss))
	for _, s := range ss {
		out = append(out, toSessionJSON(s, now))
	}
	return out
}
