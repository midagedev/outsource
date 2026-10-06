#!/usr/bin/env python3
"""probe-real-cli.py — one real `claude -p` round through the launcher, end to end.

What it checks, on the real CLI rather than a fake (the Go tests and the shell
suites only ever run fakes):

  notice  the round's first prompt opens with the launcher notice, naming the
          lead's socket and the per-launch lead token, followed by the spec;
  inbox   the round binds and reveals ITS OWN inbox (/tmp/cc-socks/<child>.sock),
          not the launching session's — the launcher strips the lead's
          CLAUDE_CODE_MESSAGING_* from the child, and this is the run that shows
          the real CLI still opens an inbox without them;
  stop    `outsource runs stop` ends the real child: the CLI catches TERM and
          exits 143, and the sentinel says rc=143, harness_signal=TERM,
          signal_source=lead-stop, stopped_by=lead — and never the token.

Why it exists: on 2026-10-06 every check of the lead-notice/stop work used a
fake harness, and the one question a fake cannot answer — does the real CLI
still reveal its own inbox once the lead's variables are gone — was settled by
a hand-run probe. This is that probe, kept.

Gating: this is a manual probe. It lives in tools/, not tests/, so
tests/run-all.sh (which runs tests/*.test.sh only) never runs it. It starts a
real `claude` process, so it also refuses inside a delegated round.

Isolation: the registry, the state dir and the config dir are a fresh temp dir;
the "lead" socket is a stand-in path that never exists, so no message can reach
a real session; the API endpoint is a local listener that accepts and never
answers, so no request leaves the machine and no quota is spent. The only
process it signals is its own round, through `runs stop`.

Usage: python3 tools/probe-real-cli.py [--bin <outsource binary>] [--keep]
Exit:  0 every check MATCH · 1 a check MISMATCH · 2 cannot run here · 64 refused
"""
import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_BIN = os.path.join(os.path.dirname(HERE), "skills", "outsource", "bin", "outsource")
LABEL = "probe-real-cli"
NOTICE_HEAD = "## Launcher notice: your lead"
# The launcher's own markers and the round's provider settings: a probe must
# answer the same from a lead's shell and from anywhere else.
DROP = ("OUTSOURCE_ROUND", "OUTSOURCE_DETACHED", "OUTSOURCE_ALLOW_NESTED", "ANTHROPIC_AUTH_TOKEN",
        "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_MAX_OUTPUT_TOKENS",
        "CLAUDE_CODE_MAX_CONTEXT_TOKENS", "OUTSOURCE_RUNS_DIR", "XDG_STATE_HOME")


def silent_listener():
    """A local API endpoint that accepts connections and never answers."""
    srv = socket.socket()
    srv.bind(("127.0.0.1", 0))
    srv.listen(16)
    held = []

    def accept():
        while True:
            try:
                conn, _ = srv.accept()
                held.append(conn)
            except OSError:
                return

    threading.Thread(target=accept, daemon=True).start()
    return srv, held


def first_user_text(transcript):
    """The text of the first user message in a claude-code transcript."""
    for line in open(transcript, encoding="utf-8"):
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if ev.get("type") != "user":
            continue
        content = (ev.get("message") or {}).get("content")
        if isinstance(content, str):
            return content
        if isinstance(content, list):
            return "".join(p.get("text", "") for p in content if isinstance(p, dict))
    return ""


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--bin", default=DEFAULT_BIN, help="the outsource binary to probe (default: this tree's)")
    ap.add_argument("--keep", action="store_true", help="keep the temp dir even when every check matches")
    a = ap.parse_args()

    if os.environ.get("OUTSOURCE_ROUND") == "1":
        print("probe-real-cli: refusing inside a delegated round (OUTSOURCE_ROUND=1) — it starts a real claude; run it from the lead's shell")
        return 64
    if shutil.which("claude") is None:
        print("probe-real-cli: no `claude` on PATH — nothing real to probe")
        return 2
    if not os.access(a.bin, os.X_OK):
        print("probe-real-cli: not an executable binary: %s" % a.bin)
        return 2

    tmp = tempfile.mkdtemp(prefix="probe-real-cli-")
    for d in ("cwd", "cfg", "state"):
        os.makedirs(os.path.join(tmp, d))
    spec_body = "# Task\nprobe-real-cli: say hello.\n"
    spec = os.path.join(tmp, "spec.md")
    open(spec, "w").write(spec_body)
    log = os.path.join(tmp, "run.log")
    stand_in = os.path.join(tmp, "lead-stand-in.sock")  # never created
    srv, held = silent_listener()

    env = {k: v for k, v in os.environ.items() if k not in DROP}
    env.update({
        "OUTSOURCE_RUNS_DIR": os.path.join(tmp, "runs"),
        "XDG_STATE_HOME": os.path.join(tmp, "state"),
        "ZAI_API_KEY": "probe-real-cli-not-a-key",
        "ZAI_ANTHROPIC_BASE": "http://127.0.0.1:%d" % srv.getsockname()[1],
        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
        "DISABLE_TELEMETRY": "1",
        "OUTSOURCE_TELEMETRY": "0",
        "CLAUDE_CODE_MESSAGING_SOCKET": stand_in,
        "CLAUDE_CODE_MESSAGING_TOKEN": "probe-stand-in-lead-token",
    })

    def run(*argv, timeout=120):
        return subprocess.run([a.bin, *argv], env=env, capture_output=True, text=True, timeout=timeout)

    def record():
        out = run("runs", "json").stdout
        try:
            rows = [r for r in json.loads(out) if r.get("label") == LABEL]
        except ValueError:
            return None
        return rows[-1] if rows else None

    verdicts = []

    def verdict(name, ok, evidence):
        verdicts.append(ok)
        print("%-7s %s  %s" % (name, "MATCH" if ok else "MISMATCH", evidence))

    stopped = False
    try:
        launch = run("outsource-run", "--cwd", os.path.join(tmp, "cwd"), "--spec", spec, "--log", log,
                     "--label", LABEL, "--config-dir", os.path.join(tmp, "cfg"), "--provider", "zai", "--detach")
        print("launch rc=%d %s" % (launch.returncode, (launch.stdout + launch.stderr).strip()))
        if launch.returncode != 0:
            return 1

        rec = None
        deadline = time.time() + 60
        while time.time() < deadline:
            rec = record()
            if rec and rec.get("messagingSocket") and rec.get("trail"):
                break
            time.sleep(0.2)
        if not rec:
            print("probe-real-cli: the round never registered")
            return 1
        token = rec.get("leadToken") or ""

        # The hook reveals the transcript path before the CLI has created the
        # file; the first user turn lands in it a few seconds later.
        prompt = ""
        deadline = time.time() + 30
        while rec.get("trail") and time.time() < deadline:
            if os.path.exists(rec["trail"]):
                prompt = first_user_text(rec["trail"])
                if prompt:
                    break
            time.sleep(0.2)
        verdict("notice", prompt.startswith(NOTICE_HEAD) and ("`uds:%s`" % stand_in) in prompt
                and ("`lead-token: %s`" % token) in prompt and len(token) == 32
                and prompt.rstrip().endswith(spec_body.rstrip()),
                "first prompt: %r…" % prompt[:72])

        want = "/tmp/cc-socks/%s.sock" % rec.get("childPid")
        verdict("inbox", rec.get("messagingSocket") == want and rec.get("messagingSocket") != stand_in,
                "messagingSocket=%s childPid=%s (the stand-in lead socket is %s); API connections held: %d"
                % (rec.get("messagingSocket"), rec.get("childPid"), stand_in, len(held)))

        stop = run("runs", "stop", LABEL, "--reason", "probe-real-cli")
        stopped = stop.returncode == 0
        try:
            body = open(log + ".rc").read()
        except OSError as e:
            body = "no sentinel: %s" % e
        lines = body.splitlines()
        want_lines = ("rc=143", "harness_signal=TERM", "signal_source=lead-stop", "stopped_by=lead")
        verdict("stop", stop.returncode == 0 and all(l in lines for l in want_lines) and token not in body,
                "runs stop rc=%d; sentinel: %s" % (stop.returncode,
                                                   " ".join(l for l in lines if l.split("=")[0] in
                                                            ("rc", "harness_signal", "signal_source", "stopped_by", "lead_socket"))))
    finally:
        if not stopped:
            # Never leave our own round behind: it is the only thing this stops.
            cur = record()
            if cur and cur.get("state") == "running":
                run("runs", "stop", LABEL, "--reason", "probe-real-cli cleanup", "--kill-after", "5")
        srv.close()

    ok = bool(verdicts) and all(verdicts)
    if ok and not a.keep:
        shutil.rmtree(tmp, ignore_errors=True)
    else:
        print("kept: %s" % tmp)
    print("probe-real-cli: %s" % ("all MATCH" if ok else "MISMATCH"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
