package runs

import (
	"bytes"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// RxProbe is what a stalled round's socket is doing. The trail only records
// completed turns, so one long streaming turn — a model thinking for ten
// minutes, or writing a large file in one tool call — looks exactly like a
// hang in the idle column. Measured 2026-09-15: a claude-code round on z.ai
// (glm-5.3, effort high, a 45 KB spec) wrote nothing to its trail for 15
// minutes after its fourth tool call and was killed as stuck on that
// evidence plus 0 % CPU and an open socket — its bytes were never sampled.
// Its fresh relaunch stopped at the same point, and there the socket was
// sampled: ~10-12 KB/s inbound, 6 MB at 11 minutes and 7.7 MB at 13, while
// the trail and the work tree stayed unchanged. CPU is no signal (the
// harness idles in a read); bytes arriving from the API are the only thing
// visible from outside that separates a long turn from a hang.
type RxProbe struct {
	BytesPerSec float64
	Window      time.Duration
	Pids        []string // the harness processes sampled
}

// Receiving is true when the sampled processes took in more than a trickle
// of keep-alive traffic over the window.
func (p RxProbe) Receiving() bool { return p.BytesPerSec >= 200 }

// probeRx samples inbound bytes on the round's harness processes (the
// launcher pid's children — the launcher itself holds no API socket). Only
// macOS has a per-process counter without root (nettop); elsewhere ok is
// false and the caller says so rather than guessing.
func probeRx(launcherPid string, window time.Duration) (RxProbe, bool) {
	if runtime.GOOS != "darwin" || launcherPid == "" {
		return RxProbe{}, false
	}
	out, err := exec.Command("pgrep", "-P", launcherPid).Output()
	if err != nil {
		return RxProbe{}, false
	}
	pids := strings.Fields(string(out))
	if len(pids) == 0 {
		return RxProbe{}, false
	}
	args := []string{"-L", "2", "-s", strconv.Itoa(int(window.Seconds())), "-J", "bytes_in", "-x"}
	for _, p := range pids {
		args = append(args, "-p", p)
	}
	out, err = exec.Command("nettop", args...).Output()
	if err != nil {
		return RxProbe{}, false
	}
	delta, ok := nettopDelta(out)
	if !ok {
		return RxProbe{}, false
	}
	return RxProbe{BytesPerSec: float64(delta) / window.Seconds(), Window: window, Pids: pids}, true
}

// nettopDelta reads `nettop -L 2 -J bytes_in -x` output: two samples, each a
// header line plus one row per process and per socket, bytes_in cumulative
// since the socket opened. The delta is the sum over socket rows (they carry
// the traffic; a process row repeats its sockets' total) between the two
// samples. Rows for the same socket are matched by their first field.
func nettopDelta(out []byte) (int64, bool) {
	var samples [][]string
	for _, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "time,") || strings.HasPrefix(line, ",") {
			samples = append(samples, nil)
			continue
		}
		if len(samples) == 0 {
			samples = append(samples, nil)
		}
		samples[len(samples)-1] = append(samples[len(samples)-1], line)
	}
	if len(samples) < 2 {
		return 0, false
	}
	first, last := sockets(samples[0]), sockets(samples[len(samples)-1])
	var delta int64
	seen := false
	for k, v := range last {
		if v0, ok := first[k]; ok {
			delta += v - v0
			seen = true
		}
	}
	return delta, seen
}

// sockets keeps the socket rows (tcp4/tcp6/udp4/udp6 …) of one sample, keyed
// by the connection field, valued by cumulative bytes_in.
func sockets(rows []string) map[string]int64 {
	m := map[string]int64{}
	for _, r := range rows {
		f := strings.Split(r, ",")
		if len(f) < 2 {
			continue
		}
		name := f[0]
		if !(strings.HasPrefix(name, "tcp") || strings.HasPrefix(name, "udp")) {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
		if err != nil {
			continue
		}
		m[name] = n
	}
	return m
}
