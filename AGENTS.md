# AGENTS.md — RCSS-tui

Compact guidance for agents working in this repo. Prefer executable sources of truth (CI, `go.mod`, code) over this file when they conflict.

## What this is

RCSS-tui is a pure-Go Bubbletea TUI + headless CLI for per-project backups via the `rclone` binary. It targets Linux, macOS, and Windows from a single module at the repo root.

- `rclone` is the only runtime dependency; credentials stay in rclone's own config.
- The original Bash scripts live in the separate `dougmb/RCSS` repo, **not here**.
- Module path: `github.com/dougmb/rcss-tui`; Go version in `go.mod`.

## Build / verify

```bash
go build ./...        # build all packages
go vet ./...          # keep clean
go test -race ./...   # what CI runs on all three OSes
go build -o rcss ./cmd/rcss    # produce the local binary
```

CI (`.github/workflows/ci.yml`) runs `go build ./...`, `go vet ./...`, `go test -race ./...` on `ubuntu-latest`, `macos-latest`, `windows-latest`. No lint/typecheck step beyond `vet`.

## Entrypoints

- `rcss` (no args) → launches the TUI.
- `rcss upload [-v] [-p] [--folder DIR] [--account NAME]` → headless upload; `--folder` is repeatable and limits the run to those source folders.
- `rcss clean [-v] [--dry-run] [--force] [--account NAME]` → headless clean.
- `--account` is the rclone remote name (e.g. `drive:`); defaults to the active account.

`rclone` absence is fatal for `upload`/`clean` but **not** for the TUI: the UI opens and locks the cloud screens.

## Architecture

```
cmd/rcss/    no args → TUI; upload/clean → headless
config/      ~/.config/rcss/config.toml model + Load/Save; multi-account Store
rclone/      thin exec wrapper: ListRemotes, Lsf, Copy, Delete, EnsureInstalled
backup/      Upload, Clean, Restore, Logger, LastRun status parsing
scheduler/   OS scheduler integration; build-tagged per OS (crontab / Task Scheduler)
tui/         Bubbletea root model + one file per screen + styles
```

Cross-platform rules: use `path/filepath`, `os.UserConfigDir`, no shelling to `sh`; OS-specific code lives in `scheduler/` behind `//go:build` tags.

## Config / accounts

- Single file: `~/.config/rcss/config.toml` (XDG-aware).
- Top-level `active_account` + `[[accounts]]` array. Each account is keyed by its `remote_name` (e.g. `drive:`).
- Accounts are fully isolated: each has its own `source_folders`, `remote_destination`, retention, `ignored_folders`, and per-account log (`backup-<account>.log` by default via `ResolveLogFile`).
- `LoadStore()` creates the file on first run and migrates a legacy flat single-account config into one account.
- The TUI root keeps `*Store` and a copy of the active `Config` (`m.cfg`); edit the active account through Settings/Folder, switch via Account.

Keep retention concepts separate:

- `retention_days` → local cleanup after a successful upload.
- `remote_retention_days` → cloud cleanup during `Clean`.
- `remote_cleanup_safety_days` → safety lock; Clean aborts without a backup newer than this.

## Safety invariants — do not break

1. **Upload local cleanup runs only inside the success branch** of `rclone copy`. A failed upload increments errors and continues; it must never delete local files.
2. **Clean has a safety lock**: it confirms a backup newer than `RemoteCleanupSafetyDays` exists (`rclone lsf --max-age`) and aborts with `ErrNoRecentBackup` otherwise. `--force` bypasses it and is dangerous.
3. **Clean previews with dry-run before real deletion** in the UI, and deletes only cloud files. Local pruning is done by Back Up Now.
4. **Restore logs to the terminal only** (`NewLogger("")`); it must not append to the backup log.
5. **Scheduling owns only RCSS-managed entries**, per account. On Unix, rewrite only the `# >>> RCSS-managed >>>` … `# <<< RCSS-managed <<<` block. On Windows, an account's tasks are enumerated by the `RCSS-<account>-` prefix; per-folder uploads add a `-<base>-<hash>` suffix. Jobs may carry a `Folder` (`--folder`) limiting them to one source folder.

## TUI conventions

- Minimum terminal size: **80×14**. Below that the UI renders only a centered warning.
- Root model frames four bordered boxes: header + menu on the left, detail + tooltip on the right, plus a footer.
- Sub-models are value types with `Update`/`View`; they signal upward via small messages (`switchScreenMsg`, `goBackMsg`, `settingsSavedMsg`, etc.).
- Sub-models that get sized on `WindowSizeMsg` (lists, `huh` forms, viewport) **must be initialized in `New`** to avoid zero-value panics.
- Streaming backup operations use `tui/stream.go` `opStream`: goroutine writes lines through the Logger sink; `opStream.wait()` delivers `opEvent`s.
- Reuse `styles.go`; don't inline lipgloss colors.

## Testing

- Tests are intentionally light and spread across packages: `backup/status_test.go`, `backup/upload_test.go`, `backup/restore_test.go`, `config/store_test.go`, `scheduler/*_test.go`, `tui/app_test.go`, `tui/settings_test.go`, `tui/schedule_test.go`.
- `config` tests use in-memory stores; they do not touch the real config file.
- Beyond unit tests, verify backup/TUI flows by placing a **fake `rclone` on the PATH** that echoes canned `lsf`/`copy`/`delete` output.
- Drive TUI sub-models headlessly by constructing them and feeding `tea.Msg` values into `Update`, then inspect `View()`.

## Release

- Releases are built by GoReleaser (`.goreleaser.yaml`) from `v*` tags via `.github/workflows/release.yml`.
- Do not hand-build release artifacts.
- Local snapshot: `goreleaser release --snapshot --clean`.
