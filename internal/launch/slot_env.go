package launch

import (
	"os"
	"path/filepath"
)

// The two names a round uses to share the machine's heavy steps (outsource
// slot, internal/slot): where the tool is, and what to call this round in the
// slot holder records, so `outsource slot --status` names rounds the way
// `outsource runs` does.
const (
	slotEnvKey     = "OUTSOURCE_SLOT"
	runLabelEnvKey = "OUTSOURCE_RUN_LABEL"
)

// exportSlotEnv puts OUTSOURCE_SLOT and OUTSOURCE_RUN_LABEL into the
// launcher's own environment once the round's label is known. Every harness
// child's environment is built from os.Environ() (claude-code, crush,
// opencode, muse, agy), so one call before dispatch reaches all of them. It
// is not done in nestedEnv, which every child also passes through, because
// the label is not in scope there.
//
// OUTSOURCE_SLOT is set only when the shim exists, so a round can read "set"
// as "usable": a path that does not resolve would turn every wrapped proof
// step into "No such file or directory".
func exportSlotEnv(label string) {
	if p := slotScript(); p != "" {
		os.Setenv(slotEnvKey, p)
	}
	if label != "" {
		os.Setenv(runLabelEnvKey, label)
	}
}

// slotScript is the absolute path of the skill's bin/slot.sh: under
// OUTSOURCE_SKILL_DIR when that is set, else beside this executable — the
// rule of quota's siblingScript. The dispatcher runs this binary from its
// download cache when there is no local build, and no shims sit beside a
// cached binary; the dispatcher exports OUTSOURCE_SKILL_DIR for that case.
func slotScript() string {
	var dirs []string
	if d := os.Getenv("OUTSOURCE_SKILL_DIR"); d != "" {
		dirs = append(dirs, filepath.Join(d, "bin"))
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, d := range dirs {
		p := filepath.Join(d, "slot.sh")
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return ""
}
