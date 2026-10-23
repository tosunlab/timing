package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tosunlab/timing/internal/claude"
	"github.com/tosunlab/timing/internal/store"
)

const maxHookTaskRunes = 80

func (a *app) hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Handle a Claude Code hook event read from stdin",
		// Claude Code treats exit 2 from a Stop hook as "do not stop", so this command never reports
		// a usage error. ArbitraryArgs keeps cobra from rejecting stray arguments with one.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.handleHook(); err != nil {
				// A fresh error drops the store sentinels that exitCode maps to exit 2.
				return errors.New(err.Error())
			}
			return nil
		},
	}
	cmd.AddCommand(a.hookInstallCmd())
	return cmd
}

func (a *app) hookInstallCmd() *cobra.Command {
	var settings string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Add timing's hooks to Claude Code's settings file",
		Args:  positional(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := settings
			if path == "" {
				home := a.io.Env("HOME")
				if home == "" {
					return usagef("HOME is not set; pass --settings")
				}
				path = filepath.Join(home, ".claude", "settings.json")
			}
			added, err := claude.Install(path)
			if err != nil {
				return err
			}
			if len(added) == 0 {
				fmt.Fprintln(a.io.Stdout, "already installed")
			}
			for _, ev := range added {
				fmt.Fprintf(a.io.Stdout, "added %s\n", ev)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&settings, "settings", "", "settings file (default $HOME/.claude/settings.json)")
	return cmd
}

func (a *app) handleHook() error {
	p, err := claude.Parse(a.io.Stdin)
	if err != nil {
		return err
	}
	switch p.Event {
	case claude.SessionStart, claude.UserPromptSubmit, claude.Stop, claude.SessionEnd:
	default:
		return nil
	}
	return a.withStore(func(st *store.Store) error {
		switch p.Event {
		case claude.SessionStart:
			_, err := a.ensureHookSession(st, p)
			return err
		case claude.UserPromptSubmit:
			s, err := a.ensureHookSession(st, p)
			if err != nil || s.Task != "" {
				return err
			}
			if task := taskFromPrompt(p.Prompt); task != "" {
				return st.Amend(s.ID, store.Amendment{Task: &task})
			}
			return nil
		case claude.Stop:
			s, err := a.ensureHookSession(st, p)
			if err != nil {
				return err
			}
			return ingestTranscript(st, s.ID, p)
		default:
			return a.endHookSession(st, p)
		}
	})
}

func (a *app) newestHookSession(st *store.Store, p claude.Payload) (store.Session, error) {
	s, err := st.Newest(p.SessionID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, nil
	}
	return s, err
}

// Looks up by Claude's session id only, never the single-open-session fallback of resolveSession.
func (a *app) ensureHookSession(st *store.Store, p claude.Payload) (store.Session, error) {
	s, err := a.newestHookSession(st, p)
	if err != nil || (s.ID != "" && s.Open()) {
		return s, err
	}
	return a.startHookSession(st, p)
}

func (a *app) startHookSession(st *store.Store, p claude.Payload) (store.Session, error) {
	cwd := p.Cwd
	if cwd == "" {
		cwd = a.io.Cwd
	}
	agent := a.io.Env("TIMING_AGENT")
	if agent == "" {
		agent = "claude-code"
	}
	n := store.NewSession{ID: a.io.NewID(), ExternalID: p.SessionID, Project: a.resolveProject(cwd), Agent: agent, Start: a.io.Now()}
	if err := st.Start(n); err != nil {
		return store.Session{}, err
	}
	return store.Session{ID: n.ID}, nil
}

// A session that already ended is left alone: later usage in the transcript is not attributed to it.
func (a *app) endHookSession(st *store.Store, p claude.Payload) error {
	s, err := a.newestHookSession(st, p)
	if err != nil {
		return err
	}
	switch {
	case s.ID == "":
		if s, err = a.startHookSession(st, p); err != nil {
			return err
		}
	case !s.Open():
		return nil
	}
	// Close the session even when ingest fails, so a bad transcript does not leave it open forever.
	ingestErr := ingestTranscript(st, s.ID, p)
	stopErr := st.Stop(s.ID, a.io.Now(), nil)
	if errors.Is(stopErr, store.ErrAlreadyStopped) {
		stopErr = nil
	}
	return errors.Join(ingestErr, stopErr)
}

func ingestTranscript(st *store.Store, id store.SessionID, p claude.Payload) error {
	if p.TranscriptPath == "" {
		return errors.New("hook payload has no transcript_path")
	}
	recs, err := claude.Usage(p.TranscriptPath)
	if err != nil {
		return err
	}
	dir := claude.SubagentDir(p.TranscriptPath, p.SessionID)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sub, err := claude.Usage(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		recs = append(recs, sub...)
	}
	_, err = st.RecordUsageBatch(id, recs)
	return err
}

func taskFromPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if strings.HasPrefix(prompt, "/") {
		return ""
	}
	line, _, _ := strings.Cut(prompt, "\n")
	r := []rune(strings.TrimSpace(line))
	return string(r[:min(len(r), maxHookTaskRunes)])
}
