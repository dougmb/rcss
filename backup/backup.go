// Package backup ports the business logic of the original RCSS Bash scripts
// (uploadBackup.sh, cleanRemoteBackups.sh, restoreBackup.sh) to Go. It drives
// the rclone binary through the rclone package and is shared by both the TUI
// and the headless cron subcommands.
//
// The safety invariants of the original scripts are preserved:
//   - Upload performs local cleanup ONLY on a successful per-project upload.
//   - Clean refuses to delete remote files unless a recent backup exists
//     (safety lock), unless explicitly forced.
package backup

import (
	"strings"

	"github.com/dougmb/rcss/config"
)

// remoteDest returns "<remote>/<destination>" — the base remote path holding
// all project folders, e.g. "douglas:/Backups", or just "douglas:" when the
// destination is empty (backups at the account root). Delegates to
// config.RemoteBase so display and path-building stay consistent.
func remoteDest(cfg config.Config) string {
	return cfg.RemoteBase()
}

// joinRemote joins remote path segments with single slashes, trimming any
// stray slashes between parts while preserving the remote-name colon. Empty
// segments (e.g. an unset project when restoring a root-level file) are
// dropped so no double slash appears.
func joinRemote(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for i, p := range parts {
		if i == 0 {
			cleaned = append(cleaned, strings.TrimRight(p, "/"))
			continue
		}
		if trimmed := strings.Trim(p, "/"); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return strings.Join(cleaned, "/")
}

// uploadExcludes builds the rclone --exclude patterns for an upload: the
// SkipFormats patterns (files) plus each IgnoredFolders entry as a directory
// exclude ("<name>/**"), so both file formats and folder names are skipped.
func uploadExcludes(cfg config.Config) []string {
	out := formatExcludes(cfg)
	for _, f := range cfg.IgnoredFolders {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f+"/**")
		}
	}
	return out
}
