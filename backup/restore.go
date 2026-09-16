package backup

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dougmb/rcss/config"
	"github.com/dougmb/rcss/rclone"
)

// RemoteEntry is one item in a remote listing: a folder or a file.
type RemoteEntry struct {
	Name  string
	IsDir bool
}

// isDirNotFound reports whether err is rclone's "directory not found" — the
// expected state before any backup has been uploaded (the destination folder
// doesn't exist yet). Callers treat it as an empty listing, not a failure.
func isDirNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "directory not found")
}

// ListEntries lists files and directories at a path relative to the account's
// remote destination. An empty relPath lists the destination root. An absent
// directory yields an empty slice (no error). Directories are listed before
// files, each group reverse-sorted by name.
func ListEntries(ctx context.Context, cfg config.Config, rc *rclone.Client, relPath string) ([]RemoteEntry, error) {
	path := joinRemote(remoteDest(cfg), relPath)
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	lines, err := rc.Lsf(ctx, path, rclone.LsfOptions{Mode: rclone.LsfAll})
	if isDirNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var dirs, files []RemoteEntry
	for _, l := range lines {
		if strings.HasSuffix(l, "/") {
			dirs = append(dirs, RemoteEntry{Name: strings.TrimSuffix(l, "/"), IsDir: true})
		} else {
			files = append(files, RemoteEntry{Name: l})
		}
	}
	reverseByName(dirs)
	reverseByName(files)
	return append(dirs, files...), nil
}

func reverseByName(es []RemoteEntry) {
	sort.Slice(es, func(i, j int) bool { return es[i].Name > es[j].Name })
}

// RestoreOptions tweaks a Restore run.
type RestoreOptions struct {
	// ShowProgress adds rclone's -P progress output.
	ShowProgress bool
	// Verbose adds rclone's -v output.
	Verbose bool
	// DryRun previews the download without writing files.
	DryRun bool
	// OutputPath overrides the download destination. When empty, the file is
	// restored to RestoreTarget(cfg, relPath) (see that function).
	OutputPath string
	// IsDir tells Restore that relPath points to a directory, so the item is
	// created inside OutputPath/RestoreTarget rather than copied directly into
	// it. Ignored when relPath is empty (restore the whole destination).
	IsDir bool
}

// Restore downloads the remote item at relPath (relative to the account's
// remote destination) to a local destination. relPath may be a file or a
// directory; empty relPath restores the entire destination. The destination
// directory is created if needed.
func Restore(ctx context.Context, cfg config.Config, rc *rclone.Client, log *Logger, relPath string, opts RestoreOptions) error {
	localPath := opts.OutputPath
	if localPath == "" {
		var err error
		if localPath, err = RestoreTarget(cfg, relPath); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", localPath, err)
	}

	src := joinRemote(remoteDest(cfg), relPath)

	// Build the local destination so the restored item lands inside the chosen
	// folder. For a directory we create a sub-folder named after the remote item;
	// for a file we copy into the chosen folder. An empty relPath restores the
	// whole remote destination directly into the chosen folder.
	dst := localPath
	if opts.IsDir && relPath != "" {
		dst = joinRemoteLocal(localPath, filepath.Base(relPath))
	} else if !opts.IsDir {
		dst = localPath + "/"
	}

	log.Infof("Downloading %s to %s...", src, dst)

	copyOpts := rclone.CopyOptions{
		IgnoreTimes: true,
		Progress:    opts.ShowProgress,
		Verbose:     opts.Verbose,
		DryRun:      opts.DryRun,
	}
	if err := rc.Copy(ctx, src, dst, copyOpts, log.Raw); err != nil {
		log.Errorf("Restore failed: %v", err)
		return err
	}

	log.Infof("Procedure completed.")
	return nil
}

// RestoreTarget returns the default local folder where a remote item at relPath
// should be restored.
//
// An explicit RestoreDestination is always the base, and the item keeps the
// remote's relative structure under it (e.g. "website/index.html" →
// "<base>/website"). With no RestoreDestination configured, a backup restores to
// the source folder it came from: the first segment of relPath names a remote
// backup folder, and when a configured source folder shares that base name the
// item goes back where it originated. Only when nothing matches — a restore from
// the destination root, or a remote folder with no local counterpart — does it
// fall back to the current working directory.
func RestoreTarget(cfg config.Config, relPath string) (string, error) {
	if base := cfg.RestoreDestination; base != "" {
		return underBase(base, relPath), nil
	}
	if src, rest, ok := matchSourceFolder(cfg, relPath); ok {
		return underBase(src, rest), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting current directory: %w", err)
	}
	return underBase(cwd, relPath), nil
}

// underBase returns the parent folder the item at relPath lands in when
// restored beneath base, preserving the remote's intermediate directories.
func underBase(base, relPath string) string {
	if relPath == "" {
		return base
	}
	dir := path.Dir(relPath)
	if dir == "." || dir == "/" {
		return base
	}
	return joinRemoteLocal(base, filepath.FromSlash(dir))
}

// matchSourceFolder maps a remote path back to the source folder it was uploaded
// from. Upload stores each folder at <dest>/<folder-basename>, so the first
// segment of relPath is that basename; rest is the path within it. Returns
// ok=false when relPath names no folder or no configured source matches.
func matchSourceFolder(cfg config.Config, relPath string) (src, rest string, ok bool) {
	relPath = strings.Trim(relPath, "/")
	if relPath == "" {
		return "", "", false
	}
	top, rest, _ := strings.Cut(relPath, "/")
	for _, f := range cfg.SourceFolders {
		if filepath.Base(f) == top {
			return f, rest, true
		}
	}
	return "", "", false
}

// joinRemoteLocal joins a local base path with a relative sub-path, keeping it
// OS-appropriate. (Local paths use the filesystem separator, not the remote
// slash join.)
func joinRemoteLocal(base, rel string) string {
	rel = strings.Trim(rel, string(os.PathSeparator))
	if rel == "" || rel == "." {
		return base
	}
	return strings.TrimRight(base, string(os.PathSeparator)) + string(os.PathSeparator) + rel
}
