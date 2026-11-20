package launch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// keepLog opens a second handle on the round's log just before the child
// starts. The round runs in a directory it may clean, and the log can sit in
// it: measured 2026-11-21, a round's scratch cleanup ran `rm -f …/round.log`
// as its last step, and the launcher then read no session, no model evidence
// and no done marker from the path, failing a finished round with exit 70. A
// handle opened before the removal still reads the unlinked file.
func keepLog(path string) *os.File {
	if path == "" || path == os.DevNull {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	return f
}

// restoreLog writes the log back to its path from the kept handle when the
// round removed the path, says so on stderr, and closes the handle. A path that
// is still there (the usual case, or a file the round wrote in its place) is
// left alone.
func restoreLog(path string, keep *os.File, stderr io.Writer) {
	if keep == nil {
		return
	}
	defer keep.Close()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "outsource: the round removed its log %s, and it cannot be put back: %v\n", path, err)
		return
	}
	defer out.Close()
	if _, err := keep.Seek(0, io.SeekStart); err != nil {
		fmt.Fprintf(stderr, "outsource: the round removed its log %s, and the kept handle cannot be read: %v\n", path, err)
		return
	}
	n, err := io.Copy(out, keep)
	if err != nil {
		fmt.Fprintf(stderr, "outsource: the round removed its log %s; restoring it stopped after %d bytes: %v\n", path, n, err)
		return
	}
	fmt.Fprintf(stderr, "outsource: the round removed its log %s; it was put back from the launcher's handle (%d bytes)\n", path, n)
}
