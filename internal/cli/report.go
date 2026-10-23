package cli

import (
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"github.com/tosunlab/timing/internal/store"
)

type scopeFlags struct {
	all   bool
	since string
}

func (a *app) filter(sf scopeFlags, now time.Time) (store.Filter, error) {
	var f store.Filter
	if !sf.all {
		f.Project = a.resolveProject()
	}
	if sf.since != "" {
		t, err := parseTime(sf.since, now)
		if err != nil {
			return f, usagef("--since: %v", err)
		}
		f.Since = t
	}
	return f, nil
}

func (a *app) statusCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show open sessions with elapsed time and tokens so far",
		Args:  positional(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			now := a.io.Now()
			f, err := a.filter(scopeFlags{all: all}, now)
			if err != nil {
				return err
			}
			f.OpenOnly = true
			return a.withStore(func(st *store.Store) error {
				ss, err := st.List(f)
				if err != nil {
					return err
				}
				switch {
				case a.json:
					return writeJSON(a.io.Stdout, sessionsJSON(ss, now))
				case len(ss) == 0:
					fmt.Fprintln(a.io.Stdout, "no open sessions")
				default:
					writeSessionTable(a, ss, now, all, false)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include every project")
	return cmd
}

func (a *app) logCmd() *cobra.Command {
	var sf scopeFlags
	var limit int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "List sessions, oldest first",
		Args:  positional(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return usagef("--limit must not be negative")
			}
			now := a.io.Now()
			f, err := a.filter(sf, now)
			if err != nil {
				return err
			}
			f.Limit = limit
			return a.withStore(func(st *store.Store) error {
				ss, err := st.List(f)
				if err != nil {
					return err
				}
				switch {
				case a.json:
					return writeJSON(a.io.Stdout, sessionsJSON(ss, now))
				case len(ss) == 0:
					fmt.Fprintln(a.io.Stdout, "no sessions")
				default:
					writeSessionTable(a, ss, now, sf.all, true)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&sf.all, "all", false, "include every project")
	cmd.Flags().IntVar(&limit, "limit", 0, "show only the newest N sessions")
	cmd.Flags().StringVar(&sf.since, "since", "", "only sessions started at or after this time")
	return cmd
}

// log shows the end time and a final duration; status shows only elapsed time so far.
func writeSessionTable(a *app, ss []store.Session, now time.Time, withProject, withEnd bool) {
	header := []string{"id"}
	if withProject {
		header = append(header, "project")
	}
	header = append(header, "agent", "task", "start")
	durationHeader := "elapsed"
	if withEnd {
		header = append(header, "end")
		durationHeader = "time"
	}
	header = append(header, durationHeader)
	header = append(header, tokenHeader...)

	rows := [][]string{header}
	for _, s := range ss {
		r := []string{string(s.ID)}
		if withProject {
			r = append(r, s.Project)
		}
		r = append(r, s.Agent, s.Task, s.Start.Format(time.RFC3339))
		if withEnd {
			end := "open"
			if !s.Open() {
				end = s.End.Format(time.RFC3339)
			}
			r = append(r, end)
		}
		r = append(r, formatDuration(s.Duration(now)))
		r = append(r, tokenCells(s.TotalUsage())...)
		rows = append(rows, r)
	}
	writeTable(a.io.Stdout, rows)
}

type groupKey func(s store.Session, now time.Time) []keyedUsage

// keyedUsage lets one session fall under several keys, which only --by model needs.
type keyedUsage struct {
	key   string
	usage store.Usage
}

var groupings = map[string]groupKey{
	"agent":   func(s store.Session, _ time.Time) []keyedUsage { return single(s.Agent, s) },
	"project": func(s store.Session, _ time.Time) []keyedUsage { return single(s.Project, s) },
	"task":    func(s store.Session, _ time.Time) []keyedUsage { return single(s.Task, s) },
	"day": func(s store.Session, now time.Time) []keyedUsage {
		return single(s.Start.In(now.Location()).Format("2006-01-02"), s)
	},
	"model": func(s store.Session, _ time.Time) []keyedUsage {
		if len(s.Usage) == 0 {
			return single("", store.Session{})
		}
		var out []keyedUsage
		for m, u := range s.Usage {
			out = append(out, keyedUsage{m, u})
		}
		return out
	},
}

func single(key string, s store.Session) []keyedUsage {
	return []keyedUsage{{key, s.TotalUsage()}}
}

type row struct {
	Key      string
	Sessions int
	Duration time.Duration
	Usage    store.Usage
}

func (r *row) add(d time.Duration, u store.Usage) {
	r.Sessions++
	r.Duration += d
	r.Usage = r.Usage.Add(u)
}

// Rows are sorted by key. The total counts each session once even when --by model files it under several keys.
func aggregate(ss []store.Session, key groupKey, now time.Time) (rows []row, total row) {
	byKey := map[string]*row{}
	for _, s := range ss {
		d := s.Duration(now)
		total.add(d, s.TotalUsage())
		for _, k := range key(s, now) {
			label := k.key
			if label == "" {
				label = "(none)"
			}
			r, ok := byKey[label]
			if !ok {
				r = &row{Key: label}
				byKey[label] = r
			}
			r.add(d, k.usage)
		}
	}
	for _, r := range byKey {
		rows = append(rows, *r)
	}
	slices.SortFunc(rows, func(a, b row) int {
		switch {
		case a.Key < b.Key:
			return -1
		case a.Key > b.Key:
			return 1
		}
		return 0
	})
	return rows, total
}

type rowJSON struct {
	Key      string     `json:"key"`
	Sessions int        `json:"sessions"`
	Seconds  int64      `json:"seconds"`
	Tokens   tokensJSON `json:"tokens"`
}

func (a *app) reportCmd() *cobra.Command {
	var sf scopeFlags
	var by string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Aggregate time and tokens",
		Args:  positional(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, ok := groupings[by]
			if !ok {
				return usagef("--by must be one of agent, day, task, model, project; got %q", by)
			}
			now := a.io.Now()
			f, err := a.filter(sf, now)
			if err != nil {
				return err
			}
			return a.withStore(func(st *store.Store) error {
				ss, err := st.List(f)
				if err != nil {
					return err
				}
				rows, total := aggregate(ss, key, now)
				if a.json {
					out := make([]rowJSON, 0, len(rows))
					for _, r := range rows {
						out = append(out, rowJSON{r.Key, r.Sessions, seconds(r.Duration), toTokensJSON(r.Usage)})
					}
					return writeJSON(a.io.Stdout, out)
				}
				total.Key = "total"
				table := [][]string{append([]string{by, "sessions", "time"}, tokenHeader...)}
				for _, r := range append(rows, total) {
					table = append(table, append([]string{r.Key, fmt.Sprint(r.Sessions), formatDuration(r.Duration)}, tokenCells(r.Usage)...))
				}
				writeTable(a.io.Stdout, table)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&sf.all, "all", false, "include every project")
	cmd.Flags().StringVar(&by, "by", "agent", "group by agent, day, task, model or project")
	cmd.Flags().StringVar(&sf.since, "since", "", "only sessions started at or after this time")
	return cmd
}
