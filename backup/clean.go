package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// ErrNoRecentBackup is returned by Clean when the safety lock trips: no backup
// newer than RemoteCleanupSafetyDays exists on the remote, so deletion is
// aborted to preserve history.
var ErrNoRecentBackup = errors.New("safety: no recent backup found on remote; cleanup aborted")

// CleanOptions tweaks a Clean run.
type CleanOptions struct {
	// DryRun previews deletions without removing anything (rclone --dry-run).
	DryRun bool
	// Force bypasses the safety lock. Dangerous: it allows deletion even when
	// no recent backup exists.
	Force bool
}

// Clean ports cleanRemoteBackups.sh: it deletes cloud files older than
// RemoteRetentionDays from <remote>/<dest>. Before deleting it enforces the
// safety lock — unless Force is set, it confirms a backup newer than
// RemoteCleanupSafetyDays exists on the remote and aborts with
// ErrNoRecentBackup otherwise, guarding against wiping history when uploads
// have silently stopped.
func Clean(ctx context.Context, cfg config.Config, rc *rclone.Client, log *Logger, opts CleanOptions) error {
	if cfg.RemoteName == "" {
		err := errors.New("config incomplete: remote_name not set")
		log.Errorf("%v", err)
		return err
	}

	remotePath := remoteDest(cfg)

	// Safety lock.
	if opts.Force {
		log.Warnf("--- FORCE MODE ENABLED: Bypassing safety lock ---")
	} else {
		log.Verbosef("Checking for recent backups (last %d days)...", cfg.RemoteCleanupSafetyDays)
		recent, err := rc.Lsf(ctx, remotePath, rclone.LsfOptions{
			Mode:      rclone.LsfFilesOnly,
			Recursive: true,
			MaxAge:    fmt.Sprintf("%dd", cfg.RemoteCleanupSafetyDays),
		})
		if err != nil {
			log.Errorf("checking recent backups: %v", err)
			return err
		}
		if len(recent) == 0 {
			log.Errorf("⚠️ SAFETY: No recent backups found on Drive in the last %d days!", cfg.RemoteCleanupSafetyDays)
			log.Errorf("Cleanup was ABORTED to preserve existing history. Check if the backup script is running.")
			return ErrNoRecentBackup
		}
		log.Verbosef("   ✓ Recent backup detected. Proceeding...")
	}

	log.Infof("Starting cloud cleanup: %s", remotePath)
	log.Infof("Criteria: Files older than %d days.", cfg.RemoteRetentionDays)

	delOpts := rclone.DeleteOptions{
		MinAge: fmt.Sprintf("%dd", cfg.RemoteRetentionDays),
		DryRun: opts.DryRun,
	}
	if log.IsVerbose() {
		delOpts.LogLevel = "INFO"
	}
	if opts.DryRun {
		log.Warnf("--- DRY-RUN MODE ENABLED ---")
	}

	if err := rc.Delete(ctx, remotePath, delOpts, log.Raw); err != nil {
		log.Errorf("Error executing Drive cleanup.")
		return err
	}

	if opts.DryRun {
		log.Infof("Simulation completed. No files were deleted.")
	} else {
		log.Infof("Cleanup completed successfully.")
	}
	return nil
}
