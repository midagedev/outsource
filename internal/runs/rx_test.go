package runs

import "testing"

func TestNettopDelta(t *testing.T) {
	// Two samples as `nettop -L 2 -J bytes_in -x` prints them: a header row
	// per sample, a process row, then its socket rows with cumulative bytes.
	out := []byte(`time,,bytes_in,
claude.43874,6151949,
tcp4 192.168.45.149:55973<->8.213.136.251:443,6006334,
tcp4 192.168.45.149:55971<->160.79.104.10:443,145615,
time,,bytes_in,
claude.43874,6358393,
tcp4 192.168.45.149:55973<->8.213.136.251:443,6212778,
tcp4 192.168.45.149:55971<->160.79.104.10:443,145615,
`)
	d, ok := nettopDelta(out)
	if !ok || d != 206444 {
		t.Fatalf("delta = %d ok=%v, want 206444 true", d, ok)
	}
	if _, ok := nettopDelta([]byte("time,,bytes_in,\nclaude.1,5,\n")); ok {
		t.Fatal("one sample must not produce a delta")
	}
	p := RxProbe{BytesPerSec: 10322}
	if !p.Receiving() {
		t.Fatal("10 KB/s is a streaming turn")
	}
	if (RxProbe{BytesPerSec: 40}).Receiving() {
		t.Fatal("40 B/s is keep-alive, not a stream")
	}
}
