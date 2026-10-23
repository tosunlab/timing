package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const hookCommand = "timing hook"

var installEvents = []Event{SessionStart, UserPromptSubmit, Stop, SessionEnd}

// Install adds the timing hook for each event that lacks one and returns the events it added.
// It rewrites the file only when something was added. Keys it does not know are kept, but the
// file is re-indented and its keys are sorted.
func Install(path string) (added []Event, err error) {
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
		path = resolved
	}
	mode := fs.FileMode(0o644)
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if info, serr := os.Stat(path); serr == nil {
			mode = info.Mode().Perm()
		}
		if len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, &settings); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if settings == nil {
				settings = map[string]json.RawMessage{}
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}

	hooks := map[string]json.RawMessage{}
	if raw, ok := settings["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, fmt.Errorf(`%s: "hooks" must be an object: %w`, path, err)
		}
	}
	for _, ev := range installEvents {
		var groups []json.RawMessage
		if raw, ok := hooks[string(ev)]; ok {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return nil, fmt.Errorf(`%s: "hooks.%s" must be an array: %w`, path, ev, err)
			}
		}
		if hasCommand(groups) {
			continue
		}
		raw, err := json.Marshal(hookGroup(ev))
		if err != nil {
			return nil, err
		}
		if hooks[string(ev)], err = json.Marshal(append(groups, raw)); err != nil {
			return nil, err
		}
		added = append(added, ev)
	}
	if len(added) == 0 {
		return nil, nil
	}

	if settings["hooks"], err = json.Marshal(hooks); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(path, append(out, '\n'), mode); err != nil {
		return nil, err
	}
	return added, nil
}

type command struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type group struct {
	Hooks []command `json:"hooks"`
}

func hookGroup(ev Event) group {
	c := command{Type: "command", Command: hookCommand}
	if ev == SessionEnd {
		c.Timeout = 10
	}
	return group{Hooks: []command{c}}
}

func hasCommand(groups []json.RawMessage) bool {
	for _, raw := range groups {
		var g group
		if json.Unmarshal(raw, &g) != nil {
			continue
		}
		for _, c := range g.Hooks {
			if c.Command == hookCommand {
				return true
			}
		}
	}
	return false
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
