# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

RCSS (Rclone Cloud Simple Scripts) is a Go program — a Bubbletea terminal UI plus a headless CLI — for managing per-project backups on an `rclone` cloud remote (typically Google Drive). It uploads each project folder, prunes old backups locally and remotely, restores files, and schedules itself via the host OS scheduler (crontab on Unix, Task Scheduler on Windows).

The backup logic was **ported to Go from three original Bash scripts** (`uploadBackup.sh`, `cleanRemoteBackups.sh`, `restoreBackup.sh`), which remain in the repo root as read-only reference (along with `backup.env`). The Go code — not the scripts — is now the source of truth. `plan.md` documents the port plan and decisions.

`rclone` is the only runtime dependency; it stores cloud credentials in its own config. RCSS never handles API secrets.

## Build / run / verify

```bash
go build ./...        # build everything
go vet ./...          # vet — keep clean
go build -o rcss ./cmd/rcss    # produce the binary

./rcss                       # open the TUI
./rcss upload [-v] [-p] [--folder DIR]   # headless upload (what cron runs)
./rcss clean [-v] [--dry-run] [--force]

go test ./...                        # all tests
go test -race ./...                  # what CI runs (Linux/macOS/Windows matrix)
go test -run TestLastRun ./backup/   # a single test
```

Tests are light but spread across three packages: `tui/app_test.go` drives the root model headless (the pattern below — navigation, help overlay, rclone-missing lock, Clean force double-confirm, Settings save), `backup/status_test.go` covers `LastRun` block parsing, and `config/store_test.go` covers in-memory account ops and per-account log resolution (no disk I/O, so the real config is never touched). CI (`.github/workflows/ci.yml`) runs `go build`, `go vet`, and `go test -race ./...` on all three OSes, so keep code portable and race-clean. Beyond the unit tests, verify changes by building/vetting and by driving the relevant package with a **fake `rclone`** on the PATH (a script that echoes canned `lsf`/`copy`/`delete` output) — this is how the backup logic and TUI flows are exercised without a real remote. For TUI work, sub-models can be driven headless by feeding `tea.Msg`s into `Update` and inspecting `View()`.

Releases are cut by goreleaser (`.goreleaser.yaml`) from `v*` tags via `.github/workflows/release.yml` — don't hand-build release artifacts. Besides archives it builds deb/rpm/apk/archlinux packages and, when their secrets are set, publishes the AUR (`rcss-bin`, `rcss`), Homebrew cask and Scoop manifest; `install.sh`/`install.ps1` install the latest release. The entrypoint lives in `cmd/rcss` so `go install …/cmd/rcss` yields a binary named `rcss`. See `RELEASING.md`.

## Architecture

```
cmd/rcss/    entrypoint: no args → TUI; `upload`/`clean` → headless (cron). Shared backup engine either way.
config/      Store of isolated accounts (Config per rclone remote) + active account + LoadStore/Save of ~/.config/rcss/config.toml (XDG-aware) + defaults.
rclone/      thin wrapper over the rclone binary: ListRemotes, Lsf, Copy, Delete, EnsureInstalled (PATH check).
backup/      ported business logic: Upload, Clean, Restore (+ ListTopLevel/ListFiles) and the Logger.
scheduler/   install/remove RCSS jobs in the OS scheduler; cross-platform Job API with per-OS backends.
tui/         Bubbletea root model (app.go) + styles.go + one file per screen.
```

**Cross-platform**: the code targets Linux, macOS, and Windows. Keep it portable — use `path/filepath` and `os.UserConfigDir`, never shell out to `sh`, and put any OS-specific code behind build tags (see `scheduler/`). `EnsureInstalled` is fatal for the headless `upload`/`clean` subcommands but **not** for the TUI: `main.go`'s `runTUI` opens the UI even when rclone is absent, and the root model records `rcloneMissing` to warn and lock the cloud screens (`tui/app.go`).

**Config / accounts**: a single `~/.config/rcss/config.toml` holds a `Store` — `active_account` plus an `[[accounts]]` array. Each account is a `Config` for one rclone remote (the `RemoteName` is the key) and is **fully isolated**: its own `SourceFolders`, `RemoteDestination`, retention, ignored folders, and per-account log (`backup-<account>.log` by default — see `ResolveLogFile`). `config.LoadStore()` creates the file on first run and **migrates a legacy flat single-account config** into one account. The TUI root holds the `*Store` and a copy of the active account's `Config` (`m.cfg`); the Account screen switches/forgets accounts, and Settings/Folder edit the active one — all persisted via `Store.Save()`. Headless `upload`/`clean` take `--account NAME` (defaulting to the active account); `upload` also takes a repeatable `--folder DIR` restricting the run to those source folders (an unconfigured folder is an error, never a silent no-op). Required fields are checked by `Config.Validate()`. Two distinct retention concepts — keep them separate: `RetentionDays` (local cleanup after upload) vs `RemoteRetentionDays` (cloud cleanup).

**rclone wrapper**: list-style commands use `output()` (capture stdout, stderr → error). Long operations use `stream()` — one `os.Pipe` receives stdout+stderr and a custom `bufio.SplitFunc` (`scanLinesOrCR`) splits on `\n` **and** `\r`, so `-P` progress updates surface live. All calls take a `context.Context`.

**backup package** mirrors the original scripts and ports their logging (`Logger` writes timestamped, fixed-width-level lines to the per-account backup log and to a sink callback; the upload SYNC SUMMARY block is appended verbatim, and `backup.LastRun` parses the most recent block to surface the last-backup status in the UI). The Logger sink is what makes one engine serve both the UI (sink → Bubbletea msgs) and headless mode (sink → stdout).

## Safety invariants — preserve these when editing

- **Upload performs local cleanup ONLY inside the success branch** of a project's `rclone copy`. A failed upload increments the error count and `continue`s; it must never delete local files. (`backup/upload.go`)
- **Clean enforces a safety lock**: before deleting it confirms a backup newer than `RemoteCleanupSafetyDays` exists on the remote (`rclone lsf --max-age`), aborting with `ErrNoRecentBackup` otherwise. `CleanOptions.Force` bypasses it — treat as dangerous. (`backup/clean.go`)
- **Clean previews with a dry-run before any real deletion** in the UI, and deletes only CLOUD files (local files are pruned by Back Up Now). The Force toggle (safety-lock bypass) is double-confirmed in the UI before it runs. (`tui/clean.go`)
- **Restore logs to the terminal only** (`NewLogger("")`), like the original interactive script — it does not append to the backup log. Its default target (`backup.RestoreTarget`) is `RestoreDestination` when set; otherwise the remote path's first segment is matched against the basenames of `SourceFolders` so a backup returns to the folder it came from, falling back to the working directory only when nothing matches.
- **Scheduling owns only RCSS-managed entries, per account.** Jobs carry `--account NAME` and are isolated by account. A `scheduler.Job` may also carry a `Folder`, which becomes `--folder` and limits that job to one source folder (empty = every folder); the Schedule screen renders one block per target — "All folders", one per source folder, then Clean. On Unix the managed `# >>> RCSS-managed >>>` … `# <<< RCSS-managed <<<` crontab block may hold lines for several accounts; `Apply(account, …)` rewrites only that account's lines and preserves the rest (`scheduler/crontab_unix.go`). Paths are emitted double-quoted and read back with the shared quote-aware `splitArgs` (`scheduler/scheduler.go`) — only `\"` and `\\` are escapes, so Windows paths keep their backslashes. On Windows an account's tasks are enumerated by the `RCSS-<account>-` prefix (per-folder uploads get a `-<base>-<hash>` suffix), not by a fixed pair of names (`scheduler/schtasks_windows.go`). All other crontab lines / scheduled tasks are preserved; neither backend needs root/admin.
- **Scheduled jobs must point at a stable binary.** `scheduler.Executable()` rejects a `go run` build (under the temp dir or a `go-build` dir), falls back to an installed `rcss`/`rcss-tui` on PATH, and otherwise fails with `ErrEphemeralBinary`; removing all jobs needs no binary. (`scheduler/executable.go`)
- **A scheduled job whose folder is no longer configured is shown, not hidden.** The Schedule screen renders it as a read-only "orphaned" block, warns at the top of the screen, and drops it on the next save.

## TUI conventions (Elm architecture)

- The root `Model` (`tui/app.go`) holds `width/height`, the active `screen` enum, and one sub-model per screen. It enforces the **80×14 minimum** guard (`MinWidth`/`MinHeight` in `tui/app.go`) and frames the UI as four bordered boxes: a left column of a header box (app name + active account) over the menu box, and a right column of the detail box over a tooltip box (the focused item's explanation — `tooltipText`), with a shared footer below. It routes `tea.WindowSizeMsg`/keys/screen-switches. Global keys: `ctrl+c` always quits; `q` quits; `esc` goes back.
- **Sub-models are value types** with `Update(msg) (subModel, tea.Cmd)` and `View() string`; they communicate upward with small messages (`switchScreenMsg`, `goBackMsg`, `remoteChosenMsg`, `folderChosenMsg`, `settingsSavedMsg`). The root sets `m.cfg` on these and recreates cfg-dependent sub-models on screen entry.
- Sub-models that get sized on `WindowSizeMsg` (lists, huh forms, viewport) **must be initialized in `New`**, or `SetSize`/`WithWidth` will panic on a zero value.
- Streaming backup operations use `tui/stream.go`'s `opStream`: the operation runs in a goroutine writing lines through the Logger sink; `opStream.wait()` is a command that delivers one `opEvent` at a time (buffered channel = backpressure). `finishWith` carries a typed result (e.g. `UploadResult`).
- Reuse the shared lipgloss styles in `styles.go`; don't inline colors.

## Reference scripts

`*.sh` and `backup.env` are kept as a portability reference and are **not** part of the build. When the Go port is fully trusted they can be removed (they remain in git history). Don't add new logic to them.
