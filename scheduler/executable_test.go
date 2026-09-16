package scheduler

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestResolveExecutable checks that a stable running binary is used as-is, a
// `go run` build falls back to an installed rcss on PATH, and scheduling is
// refused when only an ephemeral binary exists.
func TestResolveExecutable(t *testing.T) {
	root := t.TempDir()
	tmp := filepath.Join(root, "tmp")
	stable := filepath.Join(root, "home", "bin", "rcss")
	goRun := filepath.Join(tmp, "go-build123", "b001", "exe", "rcss-tui")
	cached := filepath.Join(root, "home", ".cache", "go-build", "d5", "x-d", "rcss-tui")

	none := func(string) (string, error) { return "", errors.New("not found") }
	onPath := func(name string) (string, error) {
		if name == "rcss" {
			return stable, nil
		}
		return "", errors.New("not found")
	}

	if got, err := resolveExecutable(stable, none, tmp); err != nil || got != stable {
		t.Errorf("stable binary: got %q, %v", got, err)
	}
	for _, eph := range []string{goRun, cached} {
		if got, err := resolveExecutable(eph, onPath, tmp); err != nil || got != stable {
			t.Errorf("%s with rcss on PATH: got %q, %v", eph, got, err)
		}
		if _, err := resolveExecutable(eph, none, tmp); !errors.Is(err, ErrEphemeralBinary) {
			t.Errorf("%s without rcss on PATH: err = %v", eph, err)
		}
	}
	// An ephemeral binary found on PATH is no better.
	ephOnPath := func(string) (string, error) { return goRun, nil }
	if _, err := resolveExecutable(cached, ephOnPath, tmp); !errors.Is(err, ErrEphemeralBinary) {
		t.Errorf("ephemeral binary on PATH accepted: err = %v", err)
	}
}
