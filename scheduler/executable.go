package scheduler

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrEphemeralBinary means rcss is running from a throwaway build (e.g. `go
// run`) and no installed copy was found, so a scheduled job would point at a
// binary that disappears.
var ErrEphemeralBinary = errors.New("rcss is running from a temporary build (go run); install it (go install / go build -o) and schedule from the installed binary")

// installedNames are the binary names searched on PATH when the running
// binary is ephemeral: the release name and the `go install` name.
var installedNames = []string{"rcss", "rcss-tui"}

// Executable returns a stable path to the rcss binary for scheduled jobs. It
// prefers the running binary, but when that is a temporary `go run` build it
// falls back to an installed rcss on PATH, and fails with ErrEphemeralBinary
// when there is none.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return resolveExecutable(exe, exec.LookPath, os.TempDir())
}

func resolveExecutable(exe string, lookPath func(string) (string, error), tempDir string) (string, error) {
	exe = canonical(exe)
	if !isEphemeral(exe, tempDir) {
		return exe, nil
	}
	for _, name := range installedNames {
		p, err := lookPath(name)
		if err != nil {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = canonical(abs)
			if !isEphemeral(p, tempDir) {
				return p, nil
			}
		}
	}
	return "", ErrEphemeralBinary
}

// canonical resolves symlinks, keeping the path unchanged when that fails.
func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// isEphemeral reports whether p is a binary the Go toolchain or the OS may
// delete: anything under the temp dir, or inside a go-build directory (the
// `go run` work dir or the build cache).
func isEphemeral(p, tempDir string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(filepath.Clean(p)), "/") {
		if strings.HasPrefix(seg, "go-build") {
			return true
		}
	}
	if tempDir == "" {
		return false
	}
	for _, t := range []string{tempDir, canonical(tempDir)} {
		if rel, err := filepath.Rel(t, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
