package audit

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The trail seal. Maka's log — and, until now, ours — has no tamper evidence:
// a transcript edited after the round ended looks exactly like one nobody
// touched. The launcher closes that by hashing the trail into the <log>.rc
// sentinel at finish (sentinelBody calls SealLines below), and this file owns
// both halves — the lines the launcher writes and the verdict a reader takes —
// so the format has one owner and cannot drift from its own check.
//
// verdicts:
//
//	ok        every recorded hash matches the current files
//	extended  the trail grew after the seal with its sealed prefix intact —
//	          a round resumed with --session appends to the same transcript
//	          (mid-round lead addition, 2026-10-06); exits 0 like ok
//	mismatch  a recorded hash differs, or a sealed file is gone — named
//	absent    the sentinel predates the seal, has no trail, or could not hash
//	          at finish (trail_sha256=unavailable (…))

// Seal is the verdict plus what failed, for the summary header to name.
type Seal struct {
	Verdict string // ok | mismatch | absent
	Detail  string
}

// SealLines renders the seal block the sentinel carries right after trail=:
//
//	trail_sha256=<hex>
//	trail_bytes=<n>
//	subagents_sha256=<hex>    (claude-code only, when subagents/ holds .jsonl)
//
// The empty string means "nothing to seal" (no trail recorded). Hashing must
// never fail the round: a read error becomes trail_sha256=unavailable (<reason>)
// and the round's exit code is not this function's business.
func SealLines(trail string) string {
	if trail == "" {
		return ""
	}
	data, err := os.ReadFile(trail)
	if err != nil {
		return fmt.Sprintf("trail_sha256=unavailable (%s)\n", oneLine(err.Error()))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "trail_sha256=%s\ntrail_bytes=%d\n", sha256hex(data), len(data))
	if digest, ok := subagentsDigest(strings.TrimSuffix(trail, ".jsonl") + "/subagents"); ok {
		fmt.Fprintf(&b, "subagents_sha256=%s\n", digest)
	}
	return b.String()
}

// subagentsDigest is the sha256 of the lines "<basename> <sha256>\n", one per
// subagents/*.jsonl, sorted by basename. ok=false when the directory holds no
// such files — which is every harness but claude-code, and then no line is
// written at all. One unreadable file makes the whole digest uncomputable, and
// that too is reported as unavailable rather than as a wrong answer.
func subagentsDigest(dir string) (digest string, ok bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var names []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") && !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "unavailable (" + oneLine(err.Error()) + ")", true
		}
		fmt.Fprintf(&b, "%s %s\n", n, sha256hex(data))
	}
	return sha256hex([]byte(b.String())), true
}

// ReadSeal loads the sentinel and verifies it. A sentinel that does not exist
// yet is a running round, not an error: the verdict is absent with no detail
// and there will be no termination event.
func ReadSeal(path string) (Seal, map[string]string) {
	m, err := readSentinel(path)
	if err != nil {
		return Seal{Verdict: "absent"}, nil
	}
	return verifySeal(m), m
}

// readSentinel parses the key=value sentinel into a map. Later lines win, the
// same rule runs.Record applies — finish() appends, it never rewrites.
func readSentinel(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			m[k] = v
		}
	}
	return m, sc.Err()
}

// verifySeal recomputes every recorded hash against the files as they are now.
//
// A transcript is append-only by nature: a round resumed with --session keeps
// writing the SAME trail file, so a file longer than the seal recorded is not
// tampering when the sealed PREFIX still hashes to trail_sha256 — that case is
// its own verdict, "extended", which exits 0 like ok. Shorter than recorded,
// or the prefix itself differs, is a mismatch: those are edits, not appends.
func verifySeal(m map[string]string) Seal {
	if m["trail"] == "" {
		return Seal{Verdict: "absent", Detail: "sentinel has no trail"}
	}
	if m["trail_sha256"] == "" {
		return Seal{Verdict: "absent", Detail: "sentinel predates the trail seal"}
	}
	if strings.HasPrefix(m["trail_sha256"], "unavailable") {
		return Seal{Verdict: "absent", Detail: m["trail_sha256"]}
	}
	data, err := os.ReadFile(m["trail"])
	if err != nil {
		return Seal{Verdict: "mismatch", Detail: fmt.Sprintf("sealed trail is gone: %s", m["trail"])}
	}
	verdict, detail := sealTrailVerdict(data, m["trail_sha256"], m["trail_bytes"])
	if verdict == "mismatch" {
		return Seal{Verdict: verdict, Detail: detail}
	}
	// Subagents: any difference is a mismatch — unless the trail itself reads
	// extended, because a resumed session can add subagent files too; then the
	// difference is named inside the extended verdict instead.
	if sub := m["subagents_sha256"]; sub != "" && !strings.HasPrefix(sub, "unavailable") {
		got, ok := subagentsDigest(strings.TrimSuffix(m["trail"], ".jsonl") + "/subagents")
		if !ok || got != sub {
			if verdict == "extended" {
				return Seal{Verdict: "extended", Detail: detail + "; subagents_sha256 differs"}
			}
			return Seal{Verdict: "mismatch", Detail: "subagents_sha256 differs"}
		}
	}
	return Seal{Verdict: verdict, Detail: detail}
}

// sealTrailVerdict compares the trail bytes against the sealed hash and
// length: ok, extended (appended after the seal, prefix intact), or mismatch
// (shorter than recorded, or the sealed prefix itself changed).
func sealTrailVerdict(data []byte, wantSHA, wantBytes string) (verdict, detail string) {
	if wantBytes == "" {
		// No length recorded: only the whole-file hash can be checked, and an
		// append cannot be told from a rewrite — mismatch, never extended.
		if sha256hex(data) != wantSHA {
			return "mismatch", "trail_sha256 differs"
		}
		return "ok", ""
	}
	var sealedLen int
	if _, err := fmt.Sscanf(wantBytes, "%d", &sealedLen); err != nil {
		return "mismatch", fmt.Sprintf("trail_bytes not a number: %s", wantBytes)
	}
	switch {
	case len(data) == sealedLen:
		if sha256hex(data) != wantSHA {
			return "mismatch", "trail_sha256 differs"
		}
		return "ok", ""
	case len(data) > sealedLen:
		if sha256hex(data[:sealedLen]) != wantSHA {
			return "mismatch", "trail_sha256 differs (the sealed prefix was modified)"
		}
		return "extended", fmt.Sprintf("%d bytes appended after the seal; sealed prefix intact (a resumed session appends to the same transcript)", len(data)-sealedLen)
	default:
		return "mismatch", fmt.Sprintf("trail is shorter than the seal recorded (%s recorded, %d now)", wantBytes, len(data))
	}
}

// oneLine keeps a reason on one line; the sentinel is line-oriented.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
}
