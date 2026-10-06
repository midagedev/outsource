# hermetic-env.sh — one list of the environment names a delegated round
# leaves in a suite's shell, and the scrub that removes them.
#
# Sourced by test suites and run-all.sh; never executed, and deliberately not
# named *.test.sh (run-all.sh runs every tests/*.test.sh).
#
# Why this exists (measured 2026-10-06, two round reports lost to it): a
# delegated round's harness child inherits OUTSOURCE_ROUND=1, a real
# ANTHROPIC_AUTH_TOKEN and the launcher's ANTHROPIC_*/CLAUDE_* names, so
# tests/run-all.sh was red in every round and green in the lead's shell —
# five suites, and every round since spent its first minutes proving "this
# red is not mine". A suite must give the same answer in both shells, so it
# starts by scrubbing every name below, and tests that want one of these
# markers set it themselves afterwards.
#
# One list only: a name goes here when a round's environment can carry it AND
# (unless the launcher sets it unconditionally on every round) a measured
# suite result depends on it. Cite the failing line when adding one.
#   OUTSOURCE_ROUND            launcher marks every harness child (outsource.go:64)
#   OUTSOURCE_DETACHED         --detach re-exec child (grok.go:66)
#   OUTSOURCE_ALLOW_NESTED     never inherited: a suite must not arrive with
#                              permission to nest rounds
#   ANTHROPIC_AUTH_TOKEN       claudecode.go:124; also trips the token check
#                              at glm-interactive.test.sh:275
#   ANTHROPIC_BASE_URL         claudecode.go:123 (set on every round; no suite
#                              result measured — glm.sh overwrites it — but the
#                              lead's shell has none of these)
#   ANTHROPIC_MODEL            claudecode.go:125 (same: overwritten, scrubbed
#                              so a suite never answers to the round's model)
#   CLAUDE_CONFIG_DIR          claudecode.go:126; a suite must never write
#                              into a live round's config dir
#   CLAUDE_CODE_MAX_OUTPUT_TOKENS  the round harness sets exactly 64000, and
#                              glm.sh keeps a caller-set value (glm.sh:141), so
#                              glm-interactive.test.sh:127 read the harness's
#                              value, not the script's — a glm.sh regression on
#                              this axis was green inside rounds only
#   CLAUDE_CODE_MAX_CONTEXT_TOKENS same shape at glm.sh:125; measured: an
#                              inherited value other than 1310720 fails
#                              glm-interactive.test.sh:135
#   OUTSOURCE_SKILL_DIR        the bin/outsource dispatcher exports it when it
#                              execs a binary from its download cache, so a
#                              round launched through a cached binary carries
#                              it; measured 2026-10-06 with it set, five
#                              dispatcher.test.sh checks failed ("cache hit did
#                              not exec the cached binary with the skill dir
#                              exported"), because the caller's value wins
hermetic_env_names=(
  OUTSOURCE_ROUND
  OUTSOURCE_DETACHED
  OUTSOURCE_ALLOW_NESTED
  ANTHROPIC_AUTH_TOKEN
  ANTHROPIC_BASE_URL
  ANTHROPIC_MODEL
  CLAUDE_CONFIG_DIR
  CLAUDE_CODE_MAX_OUTPUT_TOKENS
  CLAUDE_CODE_MAX_CONTEXT_TOKENS
  OUTSOURCE_SKILL_DIR
)

# hermetic_scrub_env unsets every name on the list. Call it at the top of a
# suite, before the first assertion; tests that set a marker on purpose do so
# after this and keep their meaning.
hermetic_scrub_env() {
  local n
  for n in "${hermetic_env_names[@]}"; do
    unset "$n"
  done
}

# The subset run-all.sh poisons in the lead's shell so that shell sees the
# same marker shapes a round does. Kept inside the one list's owner, and
# checked against it by run-all.sh: a poisoned name the scrub does not clean
# would defeat the whole point. OUTSOURCE_ALLOW_NESTED is never given a value
# here — poisoning it to 1 would hand every suite permission to nest.
hermetic_poison_names=(
  OUTSOURCE_ROUND
  OUTSOURCE_DETACHED
  ANTHROPIC_AUTH_TOKEN
  OUTSOURCE_SKILL_DIR
)
