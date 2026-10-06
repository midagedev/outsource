package statusline

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Main is the statusline entry point.
//
// The --refresh dispatch comes before anything reads stdin, and that ordering
// is load-bearing: the refresher is this same program re-invoked, it inherits
// the caller's stdin, and reading stdin first would block it forever — holding
// the lock and leaving the number stuck at "…" for good. The shell version
// shipped with that bug once.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	quotaSh := siblingScript("quota.sh")
	if len(args) >= 2 && args[0] == "--refresh" && args[1] != "" {
		if err := Refresh(args[1], quotaSh); err != nil {
			fmt.Fprintf(stderr, "statusline: refresh %s: %v\n", args[1], err)
			return 1
		}
		return 0
	}
	return Render(stdin, stdout, quotaSh)
}

// siblingScript resolves a tool that still lives beside the binary. During the
// port some tools are Go and some are still shell; this is the seam, and it
// disappears as each one moves.
//
// The dispatcher runs this binary from its download cache when there is no
// local build (no binary is committed to git), and beside a cached executable
// there are no shims — they live in the skill's bin/, which the dispatcher
// exports as OUTSOURCE_SKILL_DIR for exactly this case. The executable's own
// directory stays the fallback, so a local build beside the shims behaves as
// before.
func siblingScript(name string) string {
	if dir := os.Getenv("OUTSOURCE_SKILL_DIR"); dir != "" {
		p := filepath.Join(dir, "bin", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return name
	}
	return filepath.Join(filepath.Dir(exe), name)
}
