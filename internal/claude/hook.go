// Package claude parses Claude Code hook payloads and transcripts and edits its settings file.
package claude

import (
	"encoding/json"
	"fmt"
	"io"
)

type Event string

const (
	SessionStart     Event = "SessionStart"
	UserPromptSubmit Event = "UserPromptSubmit"
	Stop             Event = "Stop"
	SessionEnd       Event = "SessionEnd"
)

// Payload holds the fields timing reads from the JSON Claude Code writes to a hook's stdin.
type Payload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Event          Event  `json:"hook_event_name"`
	Prompt         string `json:"prompt"`
}

func Parse(r io.Reader) (Payload, error) {
	var p Payload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return Payload{}, fmt.Errorf("invalid hook payload: %w", err)
	}
	if p.SessionID == "" || p.Event == "" {
		return Payload{}, fmt.Errorf("invalid hook payload: session_id and hook_event_name are required")
	}
	return p, nil
}
