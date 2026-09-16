package backup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// ErrNoRecentBackup is returned by Clean when the safety lock trips: some backup
// folder has no file newer than RemoteCleanupSafetyDays on the remote, so that
// folder was left untouched to preserve its history.
var ErrNoRecentBackup = errors.New("safety: no recent backup found on remote; cleanup aborted")

// CleanOptions tweaks a Clean run.
type CleanOptions struct {
	// DryRun previews deletions without removing anything (rclone --dry-run).
	DryRun bool
	// Force bypasses the safety lock. Dangerous: it allows deletion even when
	// no recent backup exists.
	Force bool
}

// CleanTargets returns the remote folders Clean operates on: one
// <remote>/<dest>/<folder-basename> per configured source folder — exactly
// where Upload puts them. Clean never touches anything else on the remote, so
// a destination at the account root cannot reach unrelated files. Folders
// sharing a basename map to the same remote folder and are listed once.
func CleanTargets(cfg config.Config) []string {
	seen := make(map[string]bool, len(cfg.SourceFolders))
	var out []string
	for _, f := range cfg.SourceFolders {
		name := filepath.Base(filepath.Clean(f))
		if name == "." || name == string(filepath.Separator) || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, joinRemote(remoteDest(cfg), name))
	}
	return out
}

// Clean ports cleanRemoteBackups.sh: it deletes cloud files older than
// RemoteRetentionDays from each backup folder Upload writes (see CleanTargets).
// Before deleting from a folder it enforces the safety lock — unless Force is
// set, it confirms that folder holds a file newer than RemoteCleanupSafetyDays
// and skips it otherwise, guarding against wiping a folder's history when its
// uploads have silently stopped. A skipped folder makes Clean return
// ErrNoRecentBackup after the other folders are processed.
func Clean(ctx context.Context, cfg config.Config, rc *rclone.Client, log *Logger, opts CleanOptions) error {
	if err := cfg.Validate(); err != nil {
		log.Errorf("%v", err)
		return err
	}
	if cfg.RemoteRetentionDays < 1 {
		err := errors.New("remote_retention_days must be at least 1; refusing to delete every backup")
		log.Errorf("%v", err)
		return err
	}
	if !opts.Force && cfg.RemoteCleanupSafetyDays < 1 {
		err := errors.New("remote_cleanup_safety_days must be at least 1")
		log.Errorf("%v", err)
		return err
	}

	if opts.Force {
		log.Warnf("--- FORCE MODE ENABLED: Bypassing safety lock ---")
	}
	if opts.DryRun {
		log.Warnf("--- DRY-RUN MODE ENABLED ---")
	}
	log.Infof("Starting cloud cleanup: %s", remoteDest(cfg))
	log.Infof("Criteria: Files older than %d days, inside each backup folder.", cfg.RemoteRetentionDays)

	delOpts := rclone.DeleteOptions{
		MinAge: fmt.Sprintf("%dd", cfg.RemoteRetentionDays),
		DryRun: opts.DryRun,
	}
	if log.IsVerbose() {
		delOpts.LogLevel = "INFO"
	}

	var locked, failed []string
	for _, target := range CleanTargets(cfg) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Infof("→ Cleaning %s", target)

		if !opts.Force {
			log.Verbosef("   Checking for recent backups (last %d days)...", cfg.RemoteCleanupSafetyDays)
			recent, err := rc.Lsf(ctx, target+"/", rclone.LsfOptions{
				Mode:      rclone.LsfFilesOnly,
				Recursive: true,
				MaxAge:    fmt.Sprintf("%dd", cfg.RemoteCleanupSafetyDays),
			})
			if isDirNotFound(err) {
				log.Infof("   - Nothing on the remote yet, skipping.")
				continue
			}
			if err != nil {
				log.Errorf("   checking recent backups: %v", err)
				failed = append(failed, target)
				continue
			}
			if len(recent) == 0 {
				log.Errorf("   ⚠️ SAFETY: no backup newer than %d days in %s — folder SKIPPED to preserve its history.",
					cfg.RemoteCleanupSafetyDays, target)
				locked = append(locked, target)
				continue
			}
			log.Verbosef("   ✓ Recent backup detected. Proceeding...")
		}

		if err := rc.Delete(ctx, target, delOpts, log.Raw); err != nil {
			if isDirNotFound(err) {
				log.Infof("   - Nothing on the remote yet, skipping.")
				continue
			}
			log.Errorf("   Error cleaning %s: %v", target, err)
			failed = append(failed, target)
		}
	}

	switch {
	case len(failed) > 0:
		return fmt.Errorf("cleanup failed for %s", strings.Join(failed, ", "))
	case len(locked) > 0:
		log.Errorf("Cleanup was ABORTED for %d folder(s). Check that their uploads are running.", len(locked))
		return fmt.Errorf("%w: %s", ErrNoRecentBackup, strings.Join(locked, ", "))
	case opts.DryRun:
		log.Infof("Simulation completed. No files were deleted.")
	default:
		log.Infof("Cleanup completed successfully.")
	}
	return nil
}
