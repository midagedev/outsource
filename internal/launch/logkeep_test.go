package launch

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestARemovedLogIsPutBackFromTheKeptHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "round.log")
	logf, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	keep := keepLog(path)
	if keep == nil {
		t.Fatal("no handle kept on an existing log")
	}
	// The child writes through the launcher's handle, then removes the path.
	if _, err := logf.WriteString(`{"type":"result","result":"done\nX-DONE"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	logf.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	restoreLog(path, keep, &stderr)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the log was not put back: %v", err)
	}
	if !strings.Contains(string(got), "X-DONE") {
		t.Fatalf("the restored log lost its content: %q", got)
	}
	if !strings.Contains(stderr.String(), "it was put back") {
		t.Fatalf("the restore is not named on stderr: %q", stderr.String())
	}
}

func TestALogStillInPlaceIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "round.log")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := keepLog(path)
	// The round wrote another file at the path; it is not overwritten.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("the round's own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	restoreLog(path, keep, &stderr)
	got, _ := os.ReadFile(path)
	if string(got) != "the round's own\n" || stderr.Len() != 0 {
		t.Fatalf("a present path was touched: %q, stderr %q", got, stderr.String())
	}
}

func TestNoLogNoHandle(t *testing.T) {
	if keepLog(os.DevNull) != nil || keepLog("") != nil {
		t.Fatal("a handle kept on no log")
	}
	restoreLog("unused", nil, &bytes.Buffer{})
}
