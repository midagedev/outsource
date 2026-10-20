package runs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParsePS(t *testing.T) {
	// FAIL-first: make parseEtime ignore the "dd-" part and the 1-02:03:04 row
	// reads 7384 instead of 93784.
	out := "" +
		"  101   0.0       15:02 just remote-static --host  box   --slot 3\n" +
		"  102  12.5  1-02:03:04 /usr/bin/sleep 30\n" +
		" 103 0.0 01:02:03 sh\n" +
		"garbage line\n" +
		"  104 0.0 xx:yy /bin/x\n"
	got := parsePS(out)
	if len(got) != 3 {
		t.Fatalf("parsePS rows = %d, want 3: %+v", len(got), got)
	}
	want := []Descendant{
		{101, 0, 15*60 + 2, "just remote-static --host  box   --slot 3"},
		{102, 12.5, 86400 + 2*3600 + 3*60 + 4, "/usr/bin/sleep 30"},
		{103, 0, 3723, "sh"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestClipCmdTruncates(t *testing.T) {
	long := strings.Repeat("a", 250)
	if got := []rune(clipCmd(long)); len(got) != descCmdWidth || got[len(got)-1] != '…' {
		t.Errorf("clipCmd len=%d, want %d ending in an ellipsis", len(got), descCmdWidth)
	}
	if clipCmd("short") != "short" {
		t.Error("a short command must pass through")
	}
}

func TestPickLeavesKeepsLongestRunning(t *testing.T) {
	ls := []Descendant{{Pid: 1, ElapsedSeconds: 5}, {Pid: 2, ElapsedSeconds: 50}, {Pid: 3, ElapsedSeconds: 10},
		{Pid: 4, ElapsedSeconds: 40}, {Pid: 5, ElapsedSeconds: 1}}
	got, more, _ := pickLeaves(ls)
	if len(got) != 3 || more != 2 || got[0].Pid != 2 || got[1].Pid != 4 || got[2].Pid != 3 {
		t.Errorf("pickLeaves = %+v more=%d", got, more)
	}
}

// startSleeper starts `sh -c 'sleep 30; sleep 0'` (two commands, so the shell
// forks instead of exec-ing the sleep) and kills only that process group's
// pids in cleanup.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 30; sleep 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func waitLeaf(t *testing.T, root int) []Descendant {
	t.Helper()
	for i := 0; i < 50; i++ {
		leaves, _, ok := idleDescendants(strconv.Itoa(root))
		if ok && len(leaves) > 0 {
			return leaves
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func TestIdleDescendantsFindsTheLeaf(t *testing.T) {
	cmd := startSleeper(t)
	leaves := waitLeaf(t, cmd.Process.Pid)
	if len(leaves) != 1 || !strings.Contains(leaves[0].Command, "sleep 30") {
		t.Fatalf("leaves = %+v, want the sleep 30 leaf", leaves)
	}
	t.Cleanup(func() { // the sleep itself outlives its killed parent
		_ = exec.Command("kill", strconv.FormatInt(leaves[0].Pid, 10)).Run()
	})
}

func TestIdleListNamesWhatTheHarnessRuns(t *testing.T) {
	// FAIL-first: remove the printDescendants call in render.go and the
	// "running:" line disappears from the listing.
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	t.Setenv("OUTSOURCE_RUN_STALL", "10")
	log := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(log, past, past); err != nil {
		t.Fatal(err)
	}
	cmd := startSleeper(t)
	leaves := waitLeaf(t, cmd.Process.Pid)
	if len(leaves) == 0 {
		t.Fatal("sleeper child never appeared")
	}
	t.Cleanup(func() { _ = exec.Command("kill", strconv.FormatInt(leaves[0].Pid, 10)).Run() })

	var out bytes.Buffer
	if rc := Main([]string{"start", "--pid", strconv.Itoa(os.Getpid()), "--label", "idle",
		"--provider", "zen", "--harness", "opencode", "--progress-dir", log}, &out, os.Stderr); rc != 0 {
		t.Fatalf("start rc=%d", rc)
	}
	id := strings.TrimSpace(out.String())
	if err := SetChildPid(id, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	Main([]string{"list"}, &out, os.Stderr)
	if !strings.Contains(out.String(), "running: "+strconv.FormatInt(leaves[0].Pid, 10)+" sleep 30") {
		t.Fatalf("idle round must name its leaf:\n%s", out.String())
	}
	out.Reset()
	Main([]string{"json"}, &out, os.Stderr)
	if !strings.Contains(out.String(), `"descendants":[{"pid":`+strconv.FormatInt(leaves[0].Pid, 10)) {
		t.Fatalf("runs json must carry descendants for an idle round:\n%s", out.String())
	}
}

func TestIdleListWithNoChildSaysSo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	t.Setenv("OUTSOURCE_RUN_STALL", "10")
	log := filepath.Join(dir, "stream.jsonl")
	_ = os.WriteFile(log, []byte("{}\n"), 0o644)
	past := time.Now().Add(-5 * time.Minute)
	_ = os.Chtimes(log, past, past)
	// A reaped process: its pid has no children.
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	Main([]string{"start", "--pid", strconv.Itoa(os.Getpid()), "--label", "idle2",
		"--provider", "zen", "--harness", "opencode", "--progress-dir", log}, &out, os.Stderr)
	id := strings.TrimSpace(out.String())
	_ = SetChildPid(id, c.Process.Pid)
	out.Reset()
	Main([]string{"list"}, &out, os.Stderr)
	if !strings.Contains(out.String(), "running: no child process") {
		t.Fatalf("missing the no-child line:\n%s", out.String())
	}
}
