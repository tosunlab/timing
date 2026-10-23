# timing

Never add Co-Authored-By, Claude-Session, or any AI attribution lines to commit messages or PR descriptions.

Do not add design documents (design.md or similar) to the repository.

`timing` is a Go CLI that tracks wall-clock time and LLM token usage per project and task. AI coding agents are the main callers. The `internal/store` package owns the SQLite schema, migrations and invariants, and exposes only domain methods, so no `database/sql` types or table names leave it. The `internal/cli` package owns the cobra commands, flags, project and session resolution, output formats and the mapping from errors to exit codes. `main.go` only wires `cli.Run` to the real process environment.

Verify changes with:

```
gofmt -l .
go vet ./...
go test ./...
```
