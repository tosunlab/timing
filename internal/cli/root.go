package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tosunlab/timing/internal/store"
)

const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

type IO struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Env            func(string) string
	Cwd            string
	Now            func() time.Time
	NewID          func() store.SessionID
}

func OSIO() IO {
	cwd, _ := os.Getwd()
	return IO{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Env:    os.Getenv,
		Cwd:    cwd,
		Now:    time.Now,
		NewID:  randomID,
	}
}

func randomID() store.SessionID {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return store.SessionID("s_" + hex.EncodeToString(b[:]))
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// The only place store sentinels become exit codes.
func exitCode(err error) int {
	var ue usageError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &ue),
		errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrAmbiguous),
		errors.Is(err, store.ErrClosed),
		errors.Is(err, store.ErrInvalid):
		return exitUsage
	}
	return exitRuntime
}

type app struct {
	io      IO
	db      string
	project string
	json    bool
}

func Run(args []string, streams IO) int {
	a := &app{io: streams}
	root := a.rootCommand()
	root.SetArgs(args)
	root.SetOut(streams.Stdout)
	root.SetErr(streams.Stderr)
	err := root.Execute()
	if err != nil {
		fmt.Fprintf(streams.Stderr, "timing: %s\n", err)
	}
	return exitCode(err)
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "timing",
		Short:         "Track wall-clock time and LLM token usage per project and task",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usagef("unknown command %q", args[0])
			}
			return cmd.Help()
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err.Error()} })
	pf := root.PersistentFlags()
	pf.StringVar(&a.db, "db", "", "database path (env TIMING_DB)")
	pf.StringVar(&a.project, "project", "", "project name (env TIMING_PROJECT)")
	pf.BoolVar(&a.json, "json", false, "print JSON")
	root.AddCommand(a.startCmd(), a.stopCmd(), a.tokensCmd(), a.amendCmd(), a.statusCmd(), a.logCmd(), a.reportCmd())
	return root
}

func positional(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return usageError{err.Error()}
		}
		return nil
	}
}

func (a *app) withStore(fn func(*store.Store) error) error {
	st, err := store.Open(a.dbPath())
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}

func (a *app) dbPath() string {
	if a.db != "" {
		return a.db
	}
	if p := a.io.Env("TIMING_DB"); p != "" {
		return p
	}
	if x := a.io.Env("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "timing", "timing.db")
	}
	return filepath.Join(a.io.Env("HOME"), ".local", "share", "timing", "timing.db")
}

func (a *app) resolveProject() string {
	if a.project != "" {
		return a.project
	}
	if p := a.io.Env("TIMING_PROJECT"); p != "" {
		return p
	}
	for dir := a.io.Cwd; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Base(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Base(a.io.Cwd)
}

func resolvePrefix(st *store.Store, prefix string) (store.SessionID, error) {
	id, err := st.Resolve(prefix)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("no session matches %q: %w", prefix, err)
	}
	if errors.Is(err, store.ErrAmbiguous) {
		return "", fmt.Errorf("session prefix %q matches several sessions: %w", prefix, err)
	}
	return id, err
}

// Never guesses: several open sessions is an error because parallel agents on one project are normal.
func (a *app) resolveSession(st *store.Store, flag string) (store.SessionID, error) {
	prefix := flag
	if prefix == "" {
		prefix = a.io.Env("TIMING_SESSION")
	}
	if prefix != "" {
		return resolvePrefix(st, prefix)
	}

	project := a.resolveProject()
	open, err := st.List(store.Filter{Project: project, OpenOnly: true})
	if err != nil {
		return "", err
	}
	switch len(open) {
	case 0:
		return "", usagef("no open sessions in project %q; pass --session or run timing start", project)
	case 1:
		return open[0].ID, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d open sessions in project %q; pass --session or set TIMING_SESSION:", len(open), project)
	for _, s := range open {
		fmt.Fprintf(&b, "\n  %s  %s  %s  started %s", s.ID, s.Agent, s.Task, s.Start.Format(time.RFC3339))
	}
	return "", usageError{b.String()}
}
