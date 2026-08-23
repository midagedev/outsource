#!/usr/bin/env bash
# Every repo file a doc points at must exist.
#
# The class this closes: a script gets deleted or renamed, and the docs keep
# recommending it. spec-lint.sh already does this for *specs* against a target
# repo; nothing did it for this repo's own docs, and the inventory drifted
# exactly that way (README's "What's inside" was missing two shipped tools
# when this test was written).
#
# Scope: README.md, README.ko.md, SKILL.md and references/*.md. CHANGELOG.md
# is deliberately out — it is history, and history legitimately names files
# that no longer exist.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"

python3 - <<'PY'
import glob, os, re, sys

DOCS = (["README.md", "README.ko.md", "skills/outsource/SKILL.md"]
        + sorted(glob.glob("skills/outsource/references/*.md")))

# A reference is a path into one of this repo's directories, ending in an
# extension a doc would recommend running or reading. The lookbehind keeps
# paths inside *other* trees (~/.grok/bundled/skills/..., data/logs/...) from
# matching on their tail.
PAT = re.compile(
    r"(?<![\w./~-])"
    r"((?:skills/outsource/|bin/|scripts/|references/|tests/|assets/|\.claude-plugin/)"
    r"[\w./-]+\.(?:sh|py|md|mjs|json))")

# Named by the docs on purpose, shipped never: the user-authored overlay.
ALLOWED_MISSING = {"references/local-overlay.md"}

missing = []
for doc in DOCS:
    for lineno, line in enumerate(open(doc, encoding="utf-8"), 1):
        for ref in PAT.findall(line):
            ref = ref.rstrip(".")
            if ref in ALLOWED_MISSING:
                continue
            # Docs write skill-relative paths (bin/runs.sh) and repo-relative
            # ones (scripts/grok-progress.py) interchangeably; accept either
            # base, since both are real files a reader can open.
            if os.path.exists(ref) or os.path.exists(os.path.join("skills/outsource", ref)):
                continue
            missing.append(f"{doc}:{lineno}: {ref}")

for m in missing:
    print("MISSING " + m)
if missing:
    print("doc-refs: docs point at files that do not exist (rename them together, or drop the reference)")
    sys.exit(1)
print("doc-refs: every referenced file exists")
PY
