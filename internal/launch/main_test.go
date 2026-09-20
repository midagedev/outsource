package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points the run registry at a throwaway directory for the whole
// package. A launch path registers its round before it can refuse or fail, so
// a test that forgets its own t.Setenv("OUTSOURCE_RUNS_DIR", …) writes records
// into the developer's real registry, owned by whatever session ran `go test`
// — measured 2026-09-20: one `go test ./internal/launch/` left 26 failed
// "rounds" in a live session's status line. Tests that set their own directory
// still win; this is the floor under the ones that do not.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "outsource-launch-test-runs-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch tests: cannot create a registry dir:", err)
		os.Exit(2)
	}
	os.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	rc := m.Run()
	os.RemoveAll(dir)
	os.Exit(rc)
}
