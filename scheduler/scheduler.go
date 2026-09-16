// Package scheduler installs and removes RCSS's self-scheduling entries in the
// host operating system's scheduler: the user's crontab on Unix (Linux/macOS)
// and Task Scheduler on Windows. It owns only RCSS-managed jobs — unrelated
// crontab lines or scheduled tasks are left untouched. Scheduled jobs invoke
// the rcss binary headless (e.g. `rcss upload`).
//
// The public API is platform-independent (jobs in, jobs out); each OS backend
// is selected at build time (crontab_unix.go / schtasks_windows.go) and
// translates jobs to and from the native format.
package scheduler

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Kind is the headless subcommand a scheduled job runs.
type Kind int

const (
	// Upload runs `rcss upload`.
	Upload Kind = iota
	// Clean runs `rcss clean`.
	Clean
)

// Arg returns the rcss subcommand for the kind.
func (k Kind) Arg() string {
	if k == Clean {
		return "clean"
	}
	return "upload"
}

// Title returns a human label for the kind.
func (k Kind) Title() string {
	if k == Clean {
		return "Clean"
	}
	return "Upload"
}

// Job is one scheduled RCSS run.
type Job struct {
	Kind    Kind
	Hour    int          // 0–23
	Min     int          // 0–59
	Weekly  bool         // false = daily; true = weekly
	Weekday time.Weekday // day of the weekly run (0=Sun..6=Sat); used when Weekly
	// Folder limits an Upload job to a single source folder, passed to the
	// headless run as --folder. Empty means every folder configured for the
	// account. Clean jobs ignore it: cleanup is per-account, not per-folder.
	Folder string
}

// Label names the job's target for the UI: the folder's base name for a
// per-folder upload, or "All folders" when the job covers the whole account.
func (j Job) Label() string {
	if j.Folder == "" {
		return "All folders"
	}
	return filepath.Base(j.Folder)
}

// SameTarget reports whether two jobs address the same scheduler entry — the
// same kind and, for uploads, the same folder. Backends key their entries on
// this, and the UI uses it to match a stored job to its editor block.
func (j Job) SameTarget(o Job) bool {
	if j.Kind != o.Kind {
		return false
	}
	if j.Kind == Clean {
		return true
	}
	return j.Folder == o.Folder
}

// Time renders the job's time as HH:MM, or "??:??" when unknown (a backend may
// be unable to recover the exact time).
func (j Job) Time() string {
	if j.Hour < 0 || j.Min < 0 {
		return "??:??"
	}
	return fmt.Sprintf("%02d:%02d", j.Hour, j.Min)
}

// Cadence renders the job's recurrence in words, e.g. "daily" or "weekly (Mon)".
func (j Job) Cadence() string {
	if j.Weekly {
		return "weekly (" + weekdayShort(j.Weekday) + ")"
	}
	return "daily"
}

// weekdayShort returns the three-letter label for a weekday (e.g. "Mon").
func weekdayShort(d time.Weekday) string {
	// Normalize so out-of-range values (e.g. cron's 7) wrap to a valid day.
	d = time.Weekday((int(d)%7 + 7) % 7)
	return d.String()[:3]
}

// splitArgs splits a scheduled command line into tokens the way a shell would,
// honouring double quotes. strings.Fields cannot do this, and both backends
// quote paths — so a source folder like "/home/u/My Documents" must stay a
// single token on the way back in. Inside quotes, a backslash is an escape only
// before one of the characters in escapes; every other backslash is literal.
// Each backend passes the escapes its emitter produces: the crontab quotes
// \ " $ ` and %, while Task Scheduler arguments carry none, so a Windows path
// like C:\My Projects or \\nas\share survives intact.
func splitArgs(line, escapes string) []string {
	var (
		out    []string
		cur    strings.Builder
		inTok  bool
		inQuot bool
	)
	flush := func() {
		if inTok {
			out = append(out, cur.String())
			cur.Reset()
			inTok = false
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuot = !inQuot
			inTok = true // an empty "" is still a token
		case inQuot && c == '\\' && i+1 < len(line) && strings.IndexByte(escapes, line[i+1]) >= 0:
			i++
			cur.WriteByte(line[i])
			inTok = true
		case !inQuot && (c == ' ' || c == '\t'):
			flush()
		default:
			cur.WriteByte(c)
			inTok = true
		}
	}
	flush()
	return out
}

// flagValue returns the token following the named flag, or "" when absent.
func flagValue(tokens []string, flag string) string {
	for i, tok := range tokens {
		if tok == flag && i+1 < len(tokens) {
			return tokens[i+1]
		}
	}
	return ""
}

// DaemonStatus says whether the OS component that actually runs scheduled jobs
// is up. Registering a job with a stopped scheduler succeeds and then silently
// never runs, which is the failure this exists to surface.
type DaemonStatus int

const (
	// DaemonUnknown means the state could not be determined; callers must stay
	// quiet rather than guess.
	DaemonUnknown DaemonStatus = iota
	// DaemonRunning means scheduled jobs will be executed.
	DaemonRunning
	// DaemonStopped means jobs can be registered but nothing will run them.
	DaemonStopped
)

// Daemon reports whether the OS scheduler's daemon is running, along with a
// short hint to show the user when it is not.
func Daemon() (DaemonStatus, string) { return daemonState() }

// Backend returns a human label for the active OS scheduler ("crontab" or
// "Task Scheduler"), for use in UI text.
func Backend() string { return backendName }

// Current returns the RCSS-managed jobs scheduled for the given account, or nil
// when none are scheduled. Jobs for other accounts are not returned.
func Current(account string) ([]Job, error) { return current(account) }

// Apply installs the given jobs for one account, replacing any previously
// RCSS-managed jobs for that same account while leaving other accounts' jobs
// untouched. An empty slice removes the account's jobs. exePath is the rcss
// binary the jobs run (invoked headless with --account); logPath is where a
// backend that captures stdout should append it.
func Apply(account string, jobs []Job, exePath, logPath string) error {
	return apply(account, jobs, exePath, logPath)
}
