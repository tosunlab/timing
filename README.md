# timing

## What it is

`timing` tracks wall-clock time and LLM token usage per project and task. AI coding agents call it from the shell, and people use the same commands. All projects share one SQLite database, so one `report --all` covers everything.

## Install

```
go install github.com/tosunlab/timing@latest
```

The binary is pure Go and needs no cgo. The database lives at `$XDG_DATA_HOME/timing/timing.db`, or `~/.local/share/timing/timing.db` when that is unset. Override it with `--db` or `TIMING_DB`.

## Usage

```
$ timing start --project timing --agent claude-code --task "fix auth bug"
s_3f9a1c2e
$ timing tokens --session s_3f9a1c2e --model claude-fable-5-1 --input 12000 --output 800 --cache-read 40000
$ timing stop --session s_3f9a1c2e --model claude-fable-5-1 --input 3000 --output 200
$ timing amend s_3f9a --task "fix auth token refresh"
$ timing status
no open sessions
$ timing report
agent        sessions  time     input  output  cache-read  cache-write
claude-code  1         12m 03s  15000  1000    40000       0
total        1         12m 03s  15000  1000    40000       0
$ timing report --all --by project --json
[{"key":"timing","sessions":1,"seconds":723,"tokens":{"input":15000,"output":1000,"cache_read":40000,"cache_write":0}}]
$ timing log --json
```

`start` prints only the id, so `id=$(timing start ...)` works. `stop` with token flags records the usage and closes the session in one transaction. Stopping a session twice prints "already stopped at <time>" on stderr and exits 0.

Commands:

- `start [--agent A] [--task T]`
- `stop [--session S] [--model M] [--input N] [--output N] [--cache-read N] [--cache-write N]`
- `tokens [--session S] [--model M] [--input N] [--output N] [--cache-read N] [--cache-write N] [--stdin-json]`
- `amend <session> [--project P] [--agent A] [--task T] [--start TIME] [--end TIME]`. Setting `--end` on an open session closes it.
- `status [--all]`
- `log [--all] [--limit N] [--since TIME]`. Shows the newest N sessions, oldest first.
- `report [--all] [--by agent|day|task|model|project] [--since TIME]`. The default is `--by agent`. Open sessions count their elapsed time so far. With `--by model`, a session that used several models appears under each of them with its full time, and the total row counts it once.

Root flags: `--db`, `--project`, `--json`.

TIME accepts RFC3339, `2006-01-02` or `2006-01-02 15:04` (both in local time), and relative values such as `-30m` or `-2h`.

## How project and session are resolved

The project is the first of these that is set:

1. `--project`
2. `TIMING_PROJECT`
3. the basename of the nearest directory at or above the current one that contains `.git`
4. the basename of the current directory

`status`, `log` and `report` show only the resolved project unless you pass `--all`.

The session is the first of these:

1. `--session`
2. `TIMING_SESSION`
3. the only open session in the resolved project

Both `--session` and `TIMING_SESSION` accept a unique id prefix, as git does. If several sessions are open, the command fails with exit code 2 and lists them. It never guesses, because several agents working in one project is normal. If none are open it also exits 2.

The agent for `start` is `--agent`, then `TIMING_AGENT`, then "unknown".

## JSON output contract

With `--json`, stdout holds one JSON value and nothing else. Lists are `[]` when empty, never null. Field names are stable.

A session object, printed by `start`, `amend`, `stop` and `tokens` and listed by `status` and `log`:

```
{
  "id": "s_3f9a1c2e",
  "project": "timing",
  "agent": "claude-code",
  "task": "fix auth token refresh",
  "start": "2026-10-23T09:00:00Z",
  "end": "2026-10-23T09:12:03Z",
  "open": false,
  "seconds": 723,
  "tokens": {"input": 15000, "output": 1000, "cache_read": 40000, "cache_write": 0},
  "models": {"claude-fable-5-1": {"input": 15000, "output": 1000, "cache_read": 40000, "cache_write": 0}}
}
```

- `end` is null while the session is open.
- `seconds` is the elapsed time so far for an open session.
- `tokens` is the sum over all models. `models` is keyed by model name, and usage recorded without a model uses the empty string.
- Timestamps are RFC3339 in UTC with second precision.

`report --json` prints an array of rows, sorted by key, with no total row:

```
{"key": "timing", "sessions": 1, "seconds": 723, "tokens": {"input": 15000, "output": 1000, "cache_read": 40000, "cache_write": 0}}
```

An empty key (a session with no task, or usage with no model) is reported as "(none)".

`--stdin-json` for `tokens` reads one object with exactly these fields, all optional: `model`, `input`, `output`, `cache_read`, `cache_write`. Unknown fields are an error.

## Exit codes

- 0: success, including stopping an already stopped session.
- 1: runtime error, such as a database or I/O failure.
- 2: usage error. This covers bad flags, an unknown or ambiguous session, no open session to default to, a closed session, an end time before the start, and malformed input.

Errors go to stderr as `timing: <message>`.

## Agent integration

A Claude Code `Stop` hook can record the usage of the turn that just finished. This script reads the hook payload, sums the usage of every assistant message since the last human message in the transcript, and pipes the result into `timing tokens --stdin-json`. Save it as `~/.claude/hooks/timing-tokens.sh` and make it executable.

```
#!/bin/sh
transcript=$(jq -r '.transcript_path')
jq -s -c '
  (to_entries
    | map(select(.value.type == "user" and ((.value.message.content | type) == "string"
        or ((.value.message.content | type) == "array" and .value.message.content[0].type == "text"))))
    | last | .key) as $human
  | .[$human + 1:] | map(select(.type == "assistant" and .message.usage))
  | {
      model: (last.message.model),
      input: (map(.message.usage.input_tokens) | add // 0),
      output: (map(.message.usage.output_tokens) | add // 0),
      cache_read: (map(.message.usage.cache_read_input_tokens // 0) | add // 0),
      cache_write: (map(.message.usage.cache_creation_input_tokens // 0) | add // 0)
    }' "$transcript" |
  timing tokens --stdin-json
```

Tool results are also stored as `user` entries, so the filter treats only entries with text content as human messages. Each API call in a turn carries its own `input_tokens`, so the sum is the turn's total.

Register it in `~/.claude/settings.json`:

```
{
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "~/.claude/hooks/timing-tokens.sh"}]}]
  }
}
```

Start the session at the beginning of the work and export its id so the hook finds it:

```
export TIMING_SESSION=$(timing start --agent claude-code --task "fix auth bug")
```

When several agents share a project, always set `TIMING_SESSION` or pass `--session`. Without it the hook fails with exit code 2 rather than record usage against the wrong session.
