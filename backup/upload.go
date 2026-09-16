package backup

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// UploadOptions tweaks an Upload run beyond the persisted config.
type UploadOptions struct {
	// ShowProgress adds rclone's -P progress output to the streamed lines.
	ShowProgress bool
	// Folders restricts the run to these source folders, matched against
	// cfg.SourceFolders by cleaned path. Empty (the default) uploads every
	// configured folder. A folder that is not configured is an error, so a
	// scheduled job left behind by a removed folder fails loudly instead of
	// silently backing up nothing.
	Folders []string
}

// UploadResult summarizes a completed Upload run, matching the SYNC SUMMARY
// block written to the log.
type UploadResult struct {
	Status       string // "SUCCESS" or "PARTIAL"
	Scope        string // "all folders", or the folder this run was limited to
	Duration     time.Duration
	UploadErrors int
	FilesDeleted int
	DeleteErrors int
}

// Upload ports uploadBackup.sh to the multi-folder model: it uploads each folder
// in cfg.SourceFolders to <remote>/<dest>/<folder-basename> via rclone copy.
// Local cleanup runs ONLY inside the success branch of each upload (the core
// safety invariant): when DeleteAfterUpload is off no local files are touched;
// when on, files older than RetentionDays are removed (0 days removes them all).
// A SYNC SUMMARY block is appended to the log at the end of every run.
func Upload(ctx context.Context, cfg config.Config, rc *rclone.Client, log *Logger, opts UploadOptions) (UploadResult, error) {
	res := UploadResult{Status: "SUCCESS"}

	if err := cfg.Validate(); err != nil {
		log.Errorf("%v", err)
		return res, err
	}

	folders, err := selectFolders(cfg, opts.Folders)
	if err != nil {
		log.Errorf("%v", err)
		return res, err
	}
	res.Scope = scopeLabel(opts.Folders)

	overallStart := time.Now()
	log.Infof("Starting backup synchronization...")
	log.Infof("Settings: scope=%s | folders=%d | remote=%s | delete_after_upload=%t | retention=%dd | skip_formats=%s",
		res.Scope, len(folders), cfg.RemoteName, cfg.DeleteAfterUpload, cfg.RetentionDays, strings.Join(cfg.SkipFormats, " "))
	warnDuplicateNames(folders, log)

	excludes := uploadExcludes(cfg)
	for _, folder := range folders {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		name := filepath.Base(folder)
		if fi, err := os.Stat(folder); err != nil || !fi.IsDir() {
			log.Warnf("   ⚠ Source folder not found, skipping: %s", folder)
			res.UploadErrors++
			continue
		}

		log.Infof("→ Backing up folder: %s", name)
		stepStart := time.Now()

		copyOpts := rclone.CopyOptions{
			LogLevel:     log.RcloneLogLevel(),
			StatsOneLine: true,
			Stats:        "10s",
			Update:       true,
			UseMmap:      true,
			Retries:      3,
			Progress:     opts.ShowProgress,
			Excludes:     excludes,
		}

		dst := joinRemote(cfg.RemoteName, cfg.RemoteDestination, name)
		if err := rc.Copy(ctx, folder, dst, copyOpts, log.Raw); err != nil {
			log.Warnf("   ⚠ Sync failed for %s. Local cleanup SKIPPED.", name)
			res.UploadErrors++
			log.Verbosef("   Folder time: %s", time.Since(stepStart).Round(time.Second))
			continue
		}

		log.Infof("   ✓ Synchronized successfully.")

		// Local cleanup — ONLY reached on a successful upload.
		deleted, delErrs := cleanupLocal(folder, cfg, log)
		if deleted > 0 {
			log.Infof("   - Removed %d local files.", deleted)
		}
		res.FilesDeleted += deleted
		res.DeleteErrors += delErrs

		log.Verbosef("   Folder time: %s", time.Since(stepStart).Round(time.Second))
	}

	res.Duration = time.Since(overallStart)
	if res.UploadErrors > 0 {
		res.Status = "PARTIAL"
	}

	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Infof("✅ Synchronization completed in %s", res.Duration.Round(time.Second))
	log.writeBlock(summaryBlock(cfg, res))

	if res.UploadErrors > 0 {
		return res, fmt.Errorf("%d folder(s) failed to upload", res.UploadErrors)
	}
	return res, nil
}

// selectFolders resolves the folders an Upload run should cover: every
// configured source when want is empty, otherwise exactly the wanted ones. Each
// wanted folder must be configured for the account — an unknown one is an error
// naming it, which is what turns an orphaned scheduled job into a clear log
// entry rather than a silent no-op.
func selectFolders(cfg config.Config, want []string) ([]string, error) {
	if len(want) == 0 {
		return cfg.SourceFolders, nil
	}
	configured := make(map[string]string, len(cfg.SourceFolders))
	for _, f := range cfg.SourceFolders {
		configured[filepath.Clean(f)] = f
	}
	out := make([]string, 0, len(want))
	for _, w := range want {
		f, ok := configured[filepath.Clean(w)]
		if !ok {
			return nil, fmt.Errorf("folder %q is not a configured source for account %q", w, cfg.RemoteName)
		}
		out = append(out, f)
	}
	return out, nil
}

// scopeLabel renders the run's scope for the log and the SYNC SUMMARY block.
func scopeLabel(want []string) string {
	switch len(want) {
	case 0:
		return "all folders"
	case 1:
		return filepath.Base(want[0])
	}
	names := make([]string, 0, len(want))
	for _, w := range want {
		names = append(names, filepath.Base(w))
	}
	return strings.Join(names, ", ")
}

// warnDuplicateNames logs a warning when two source folders share a basename,
// since they map to the same remote folder and would merge there.
func warnDuplicateNames(folders []string, log *Logger) {
	seen := make(map[string]bool, len(folders))
	for _, f := range folders {
		name := filepath.Base(f)
		if seen[name] {
			log.Warnf("Two source folders share the name %q; they will merge into the same remote folder.", name)
		}
		seen[name] = true
	}
}

// cleanupLocal removes top-level files from projectPath after a successful
// upload, returning the number deleted and the number of delete errors. When
// DeleteAfterUpload is off it does nothing — local files are always kept.
// When on it removes files older than RetentionDays (0 days removes them all).
// Files excluded from the upload (SkipFormats) are never deleted, since they
// were not backed up. It never recurses and never removes directories,
// matching `find -maxdepth 1 -type f`.
func cleanupLocal(projectPath string, cfg config.Config, log *Logger) (deleted, delErrs int) {
	if !cfg.DeleteAfterUpload {
		return 0, 0 // local cleanup is disabled — keep everything
	}
	if cfg.RetentionDays == 0 {
		log.Verbosef("   Deleting all uploaded local files...")
	} else {
		log.Verbosef("   Cleaning local files older than %d days...", cfg.RetentionDays)
	}

	entries, err := os.ReadDir(projectPath)
	if err != nil {
		log.Warnf("   ⚠ Could not list local files in %s: %v", projectPath, err)
		return 0, 1
	}

	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Skip irregular files (symlinks, sockets, …): -type f only.
		if !info.Mode().IsRegular() {
			continue
		}
		// Don't delete files that were excluded from the upload.
		if matchesSkip(e.Name(), cfg) {
			continue
		}
		// Keep files newer than the retention window (0 days keeps nothing).
		if cfg.RetentionDays > 0 && !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(projectPath, e.Name())
		if err := os.Remove(path); err != nil {
			log.Warnf("   ⚠ Could not delete: %s", path)
			delErrs++
			continue
		}
		deleted++
	}
	return deleted, delErrs
}

// formatExcludes maps cfg.SkipFormats to rclone --exclude patterns. A bare
// token like "tmp" becomes "*.tmp"; an entry that already looks like a pattern
// (contains a glob metacharacter or starts with a dot) is used verbatim, and
// the dotfile pattern ".*" also excludes dot-directories via ".*/**".
func formatExcludes(cfg config.Config) []string {
	var out []string
	for _, f := range cfg.SkipFormats {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if strings.HasPrefix(f, ".") || strings.ContainsAny(f, "*?[/") {
			out = append(out, f)
			if f == ".*" {
				out = append(out, ".*/**")
			}
			continue
		}
		out = append(out, "*."+f)
	}
	return out
}

// matchesSkip reports whether a top-level file name is excluded by SkipFormats,
// so local cleanup never deletes a file that was not uploaded. Patterns that
// only match directories (e.g. ".*/**") can't match a bare file name and are
// simply ignored here.
func matchesSkip(name string, cfg config.Config) bool {
	for _, pat := range formatExcludes(cfg) {
		if ok, err := path.Match(pat, name); err == nil && ok {
			return true
		}
	}
	return false
}

// summaryBlock builds the fixed-width SYNC SUMMARY appended to the log.
func summaryBlock(cfg config.Config, res UploadResult) []string {
	secs := int(res.Duration.Round(time.Second).Seconds())
	return []string{
		"════════════════════════════════════════════════",
		fmt.Sprintf("  SYNC SUMMARY — %s", time.Now().Format("2006-01-02 15:04:05")),
		"════════════════════════════════════════════════",
		fmt.Sprintf("  Status            : %s", res.Status),
		fmt.Sprintf("  Scope             : %s", res.Scope),
		fmt.Sprintf("  Duration          : %ds", secs),
		fmt.Sprintf("  Cloud Destination : %s/", remoteDest(cfg)),
		fmt.Sprintf("  Projects w/ Errors: %d", res.UploadErrors),
		fmt.Sprintf("  Files Removed (Local): %d", res.FilesDeleted),
		fmt.Sprintf("  Delete Errors     : %d", res.DeleteErrors),
		"  --- Flags ---",
		fmt.Sprintf("  delete_after_upload: %t", cfg.DeleteAfterUpload),
		fmt.Sprintf("  retention_days    : %d", cfg.RetentionDays),
		fmt.Sprintf("  skip_formats      : %s", strings.Join(cfg.SkipFormats, " ")),
		"════════════════════════════════════════════════",
		"",
	}
}
