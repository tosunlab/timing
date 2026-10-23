package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/tosunlab/timing/internal/store"
)

type tokenFlags struct {
	model                                string
	input, output, cacheRead, cacheWrite int64
	stdinJSON                            bool
	cmd                                  *cobra.Command
}

// One list of token flags shared by stop and tokens.
func addTokenFlags(cmd *cobra.Command, withStdin bool) *tokenFlags {
	t := &tokenFlags{cmd: cmd}
	f := cmd.Flags()
	f.StringVar(&t.model, "model", "", "model name")
	f.Int64Var(&t.input, "input", 0, "input tokens")
	f.Int64Var(&t.output, "output", 0, "output tokens")
	f.Int64Var(&t.cacheRead, "cache-read", 0, "cache-read tokens")
	f.Int64Var(&t.cacheWrite, "cache-write", 0, "cache-write tokens")
	if withStdin {
		f.BoolVar(&t.stdinJSON, "stdin-json", false, `read {"model","input","output","cache_read","cache_write"} from stdin`)
	}
	return t
}

func (t *tokenFlags) countsChanged() bool {
	f := t.cmd.Flags()
	return f.Changed("input") || f.Changed("output") || f.Changed("cache-read") || f.Changed("cache-write")
}

// record returns nil when no token flags were given.
func (t *tokenFlags) record(stdin io.Reader, at time.Time, required bool) (*store.UsageRecord, error) {
	if t.stdinJSON {
		if t.countsChanged() || t.cmd.Flags().Changed("model") {
			return nil, usagef("--stdin-json cannot be combined with --model or count flags")
		}
		var in struct {
			Model      string `json:"model"`
			Input      int64  `json:"input"`
			Output     int64  `json:"output"`
			CacheRead  int64  `json:"cache_read"`
			CacheWrite int64  `json:"cache_write"`
		}
		dec := json.NewDecoder(stdin)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return nil, usagef("invalid usage JSON on stdin: %v", err)
		}
		t.model, t.input, t.output, t.cacheRead, t.cacheWrite = in.Model, in.Input, in.Output, in.CacheRead, in.CacheWrite
	} else if !t.countsChanged() {
		if t.cmd.Flags().Changed("model") {
			return nil, usagef("--model needs at least one token count")
		}
		if required {
			return nil, usagef("give at least one token count or --stdin-json")
		}
		return nil, nil
	}

	u := store.Usage{Input: t.input, Output: t.output, CacheRead: t.cacheRead, CacheWrite: t.cacheWrite}
	if u.Input < 0 || u.Output < 0 || u.CacheRead < 0 || u.CacheWrite < 0 {
		return nil, usagef("token counts must not be negative")
	}
	if required && u.IsZero() {
		return nil, usagef("at least one token count must be non-zero")
	}
	return &store.UsageRecord{Model: t.model, Usage: u, At: at}, nil
}

func (a *app) startCmd() *cobra.Command {
	var agent, task string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Open a session and print its id",
		Args:  positional(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if agent == "" {
				agent = a.io.Env("TIMING_AGENT")
			}
			if agent == "" {
				agent = "unknown"
			}
			n := store.NewSession{ID: a.io.NewID(), Project: a.resolveProject(), Agent: agent, Task: task, Start: a.io.Now()}
			return a.withStore(func(st *store.Store) error {
				if err := st.Start(n); err != nil {
					return err
				}
				if a.json {
					s, err := st.Get(n.ID)
					if err != nil {
						return err
					}
					return writeJSON(a.io.Stdout, toSessionJSON(s, a.io.Now()))
				}
				_, err := fmt.Fprintln(a.io.Stdout, n.ID)
				return err
			})
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent name (env TIMING_AGENT, default unknown)")
	cmd.Flags().StringVar(&task, "task", "", "task description")
	return cmd
}

func (a *app) stopCmd() *cobra.Command {
	var session string
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Close a session, optionally recording final token usage",
		Args:  positional(cobra.NoArgs),
	}
	tf := addTokenFlags(cmd, false)
	cmd.Flags().StringVar(&session, "session", "", "session id or unique prefix (env TIMING_SESSION)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		at := a.io.Now()
		usage, err := tf.record(a.io.Stdin, at, false)
		if err != nil {
			return err
		}
		return a.withStore(func(st *store.Store) error {
			id, err := a.resolveSession(st, session)
			if err != nil {
				return err
			}
			err = st.Stop(id, at, usage)
			if errors.Is(err, store.ErrAlreadyStopped) {
				s, gerr := st.Get(id)
				if gerr != nil {
					return gerr
				}
				fmt.Fprintf(a.io.Stderr, "already stopped at %s\n", s.End.Format(time.RFC3339))
				return nil
			}
			if err != nil {
				return err
			}
			return a.printSessionIfJSON(st, id)
		})
	}
	return cmd
}

func (a *app) tokensCmd() *cobra.Command {
	var session string
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Record token usage on an open session",
		Args:  positional(cobra.NoArgs),
	}
	tf := addTokenFlags(cmd, true)
	cmd.Flags().StringVar(&session, "session", "", "session id or unique prefix (env TIMING_SESSION)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		usage, err := tf.record(a.io.Stdin, a.io.Now(), true)
		if err != nil {
			return err
		}
		return a.withStore(func(st *store.Store) error {
			id, err := a.resolveSession(st, session)
			if err != nil {
				return err
			}
			if err := st.RecordUsage(id, *usage); err != nil {
				return err
			}
			return a.printSessionIfJSON(st, id)
		})
	}
	return cmd
}

func (a *app) amendCmd() *cobra.Command {
	var project, agent, task, start, end string
	cmd := &cobra.Command{
		Use:   "amend <session>",
		Short: "Change a session's project, agent, task, start or end",
		Args:  positional(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			var am store.Amendment
			now := a.io.Now()
			if f.Changed("project") {
				if project == "" {
					return usagef("--project must not be empty")
				}
				am.Project = &project
			}
			if f.Changed("agent") {
				if agent == "" {
					return usagef("--agent must not be empty")
				}
				am.Agent = &agent
			}
			if f.Changed("task") {
				am.Task = &task
			}
			for _, tf := range []struct {
				name string
				val  string
				dst  **time.Time
			}{{"start", start, &am.Start}, {"end", end, &am.End}} {
				if !f.Changed(tf.name) {
					continue
				}
				t, err := parseTime(tf.val, now)
				if err != nil {
					return usagef("--%s: %v", tf.name, err)
				}
				*tf.dst = &t
			}
			if am == (store.Amendment{}) {
				return usagef("amend needs at least one of --project, --agent, --task, --start, --end")
			}
			return a.withStore(func(st *store.Store) error {
				id, err := resolvePrefix(st, args[0])
				if err != nil {
					return err
				}
				if err := st.Amend(id, am); err != nil {
					return err
				}
				s, err := st.Get(id)
				if err != nil {
					return err
				}
				if a.json {
					return writeJSON(a.io.Stdout, toSessionJSON(s, now))
				}
				writeSessionDetail(a.io.Stdout, s, now)
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "new project")
	f.StringVar(&agent, "agent", "", "new agent")
	f.StringVar(&task, "task", "", "new task")
	f.StringVar(&start, "start", "", "new start time")
	f.StringVar(&end, "end", "", "new end time (closes an open session)")
	return cmd
}

func (a *app) printSessionIfJSON(st *store.Store, id store.SessionID) error {
	if !a.json {
		return nil
	}
	s, err := st.Get(id)
	if err != nil {
		return err
	}
	return writeJSON(a.io.Stdout, toSessionJSON(s, a.io.Now()))
}

func writeSessionDetail(w io.Writer, s store.Session, now time.Time) {
	end := "open"
	if !s.Open() {
		end = s.End.Format(time.RFC3339)
	}
	u := s.TotalUsage()
	writeTable(w, [][]string{
		{"id", string(s.ID)},
		{"project", s.Project},
		{"agent", s.Agent},
		{"task", s.Task},
		{"start", s.Start.Format(time.RFC3339)},
		{"end", end},
		{"time", formatDuration(s.Duration(now))},
		{"tokens", fmt.Sprintf("input %d, output %d, cache-read %d, cache-write %d", u.Input, u.Output, u.CacheRead, u.CacheWrite)},
	})
}
