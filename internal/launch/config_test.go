package launch

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The user config (internal/config) as the launcher reads it. TestMain points
// OUTSOURCE_CONFIG at a missing file, so every test here writes its own.

// userConfig writes body as this test's user config and returns its path.
func userConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OUTSOURCE_CONFIG", path)
	return path
}

// cfgDetachRig isolates a --detach launch (isolateLaunch: harness-free PATH,
// private registry) with no provider, harness or model inherited from the
// caller's shell. A detached launch that passes every pre-flight refuses at
// the missing harness CLI (ExitHarnessMissing) — after the config, the model
// guards and the credential check, before anything is registered — so that
// exit code is the witness that a launch was let through. child is the same
// line without --detach, the shape the re-exec starts (the caller sets
// OUTSOURCE_DETACHED).
func cfgDetachRig(t *testing.T) (launch, child func(extra ...string) (int, string), registered func() int) {
	t.Helper()
	dir := isolateLaunch(t)
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"OUTSOURCE_PROVIDER", "OUTSOURCE_HARNESS", "GLM_DELEGATE_MODEL", "OUTSOURCE_ALLOW_MAPPED_MODEL"} {
		t.Setenv(k, "")
	}
	// opencode's auth store, so the openrouter credential pre-flight reads a
	// file this test owns rather than the developer's.
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(data, "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "opencode", "auth.json"), []byte(`{"openrouter":{"type":"api","key":"sk-or-test-not-a-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", data)
	run := func(first []string, extra []string) (int, string) {
		t.Helper()
		var errb bytes.Buffer
		args := append(first, "--cwd", dir, "--spec", spec,
			"--log", filepath.Join(dir, "l"), "--config-dir", filepath.Join(dir, "cfg"), "--label", "cfg-test")
		rc := OutsourceMain(append(args, extra...), io.Discard, &errb)
		return rc, errb.String()
	}
	launch = func(extra ...string) (int, string) { return run([]string{"--detach"}, extra) }
	child = func(extra ...string) (int, string) { return run(nil, extra) }
	registered = func() int {
		ents, _ := os.ReadDir(filepath.Join(dir, "runs"))
		return len(ents)
	}
	return launch, child, registered
}

// A provider the user disabled is refused with the message that names the
// file and the command that undoes it, before a round is registered. The fake
// crush makes the contrast real: the same launch with the provider enabled
// runs a round and registers it. FAIL-first: without the Enabled check the
// disabled launch runs (rc 0) and registers one round.
func TestUserConfigDisabledProviderRefusedBeforeTheRegistry(t *testing.T) {
	_, envOut, spec := slotEnvRig(t)
	dir := filepath.Dir(spec)
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	t.Setenv("GLM_DELEGATE_MODEL", "")
	run := func() (int, string) {
		var errb bytes.Buffer
		rc := OutsourceMain([]string{"--foreground", "--harness", "crush", "--provider", "zai",
			"--cwd", filepath.Join(dir, "cwd"), "--spec", spec, "--log", filepath.Join(dir, "round.log"),
			"--config-dir", filepath.Join(dir, "cfg"), "--label", "cfg-disabled"}, io.Discard, &errb)
		return rc, errb.String()
	}
	registered := func() int {
		ents, _ := os.ReadDir(filepath.Join(dir, "runs"))
		return len(ents)
	}

	path := userConfig(t, `{"providers": {"zai": {"enabled": false}}}`)
	rc, stderr := run()
	want := "outsource: provider zai is disabled in " + path + " (providers.zai.enabled=false) — enable it with: outsource config set providers.zai.enabled true\n"
	if rc != ExitUsage || stderr != want {
		t.Fatalf("disabled zai: rc=%d stderr=%q, want rc=%d stderr=%q", rc, stderr, ExitUsage, want)
	}
	if n := registered(); n != 0 {
		t.Fatalf("a disabled provider registered %d round(s); the refusal must come before the registry", n)
	}
	if _, err := os.Stat(envOut); err == nil {
		t.Fatal("a disabled provider's harness ran")
	}

	// Boundary: enabled=true, and another provider disabled, change nothing
	// for zai — the round runs and is registered.
	userConfig(t, `{"providers": {"zai": {"enabled": true}, "muse": {"enabled": false}}}`)
	if rc, stderr := run(); rc != 0 {
		t.Fatalf("enabled zai: rc=%d, want 0; stderr=%s", rc, stderr)
	}
	if n := registered(); n != 1 {
		t.Fatalf("the enabled launch registered %d round(s), want 1", n)
	}
}

// A config default reaches every reader the table default reaches — the
// harness's own qualification, the registry and the sentinel — and the
// precedence is --model, then the provider's model env var, then the config,
// then the table. Read from a real (fake-crush) round's sentinel and registry
// record. FAIL-first: without `p.defaultModel = m` the config row reads
// model_requested=zai/glm-5.3 and model=glm-5.3.
func TestUserConfigDefaultModelReachesEveryReader(t *testing.T) {
	_, _, spec := slotEnvRig(t)
	dir := filepath.Dir(spec)
	runsDir := filepath.Join(dir, "runs")
	t.Setenv("OUTSOURCE_RUNS_DIR", runsDir)
	cases := []struct {
		name, config, env, flag string
		sentinel, registry      string
	}{
		{"config default", `{"providers": {"zai": {"defaultModel": "glm-4.6"}}}`, "", "", "zai/glm-4.6", "glm-4.6"},
		{"--model beats the config", `{"providers": {"zai": {"defaultModel": "glm-4.6"}}}`, "", "zai/glm-5.3-flash", "zai/glm-5.3-flash", "zai/glm-5.3-flash"},
		{"GLM_DELEGATE_MODEL beats the config", `{"providers": {"zai": {"defaultModel": "glm-4.6"}}}`, "zai/glm-5.3-flash", "", "zai/glm-5.3-flash", "zai/glm-5.3-flash"},
		{"no config: the table", `{}`, "", "", "zai/glm-5.3", "glm-5.3"},
	}
	for i, c := range cases {
		userConfig(t, c.config)
		t.Setenv("GLM_DELEGATE_MODEL", c.env)
		os.RemoveAll(runsDir)
		log := filepath.Join(dir, "round"+string(rune('a'+i))+".log")
		args := []string{"--foreground", "--harness", "crush", "--provider", "zai",
			"--cwd", filepath.Join(dir, "cwd"), "--spec", spec, "--log", log,
			"--config-dir", filepath.Join(dir, "cfg"), "--label", "cfg-model"}
		if c.flag != "" {
			args = append(args, "--model", c.flag)
		}
		var errb bytes.Buffer
		if rc := OutsourceMain(args, io.Discard, &errb); rc != 0 {
			t.Fatalf("%s: rc=%d, want 0; stderr=%s", c.name, rc, errb.String())
		}
		sentinel, err := os.ReadFile(log + ".rc")
		if err != nil {
			t.Fatalf("%s: no sentinel: %v", c.name, err)
		}
		mustContain(t, c.name+": sentinel", string(sentinel), "model_requested="+c.sentinel+"\n")
		ents, _ := os.ReadDir(runsDir)
		if len(ents) != 1 {
			t.Fatalf("%s: %d registry records, want 1", c.name, len(ents))
		}
		rec, _ := os.ReadFile(filepath.Join(runsDir, ents[0].Name()))
		mustContain(t, c.name+": registry record", string(rec), "model="+c.registry+"\n")
	}
}

// A config default goes through the mapped-model guard exactly as --model
// does: glm-5.2 is answered by glm-5.3 on zai, so a config naming it is
// refused with exit 70 before the registry, and the message says the id came
// from the config, not from a --model this launch never had. FAIL-first:
// with the guard reading o.model alone (its old argument), the config row
// launches (ExitHarnessMissing) instead of exiting 70.
func TestUserConfigMappedDefaultIsRefusedLikeTheFlag(t *testing.T) {
	launch, _, registered := cfgDetachRig(t)

	// The flag, for parity.
	rc, stderr := launch("--model", "glm-5.2")
	if rc != ExitModelIdentity {
		t.Fatalf("--model glm-5.2: rc=%d, want %d; stderr=%s", rc, ExitModelIdentity, stderr)
	}

	path := userConfig(t, `{"providers": {"zai": {"defaultModel": "glm-5.2"}}}`)
	rc, stderr = launch()
	if rc != ExitModelIdentity {
		t.Fatalf("config glm-5.2: rc=%d, want %d; stderr=%s", rc, ExitModelIdentity, stderr)
	}
	mustContain(t, "config glm-5.2 refusal", stderr,
		"no --model was given; the model below is providers.zai.defaultModel in "+path,
		"glm-5.2 is silently answered by glm-5.3")
	if n := registered(); n != 0 {
		t.Fatalf("a refused mapped default registered %d round(s)", n)
	}

	// Boundary: an explicit id, by flag or by the provider's env var, is what
	// the guard sees — the config default is never consulted.
	if rc, stderr := launch("--model", "glm-5.3"); rc != ExitHarnessMissing {
		t.Fatalf("config glm-5.2 + --model glm-5.3: rc=%d, want %d (launch let through); stderr=%s", rc, ExitHarnessMissing, stderr)
	}
	t.Setenv("GLM_DELEGATE_MODEL", "glm-5.3")
	if rc, stderr := launch(); rc != ExitHarnessMissing {
		t.Fatalf("config glm-5.2 + GLM_DELEGATE_MODEL=glm-5.3: rc=%d, want %d; stderr=%s", rc, ExitHarnessMissing, stderr)
	}
}

// The --detach child re-runs OutsourceMain and reads the same file itself, so
// it reaches the parent's answer with nothing about the config passed through
// the environment. The shape of TestDetachedChildSkipsTheSecondQuotaGate: the
// parent with --detach, then the child as the re-exec starts it — no
// --detach, OUTSOURCE_DETACHED=1. FAIL-first: a config read only in the
// parent (skipped when OUTSOURCE_DETACHED=1) lets the child past the
// disabled provider and the mapped default.
func TestUserConfigDetachedChildReachesTheSameAnswer(t *testing.T) {
	launch, child, registered := cfgDetachRig(t)
	for _, c := range []struct {
		body string
		rc   int
	}{
		{`{"providers": {"zai": {"enabled": false}}}`, ExitUsage},
		{`{"providers": {"zai": {"defaultModel": "glm-5.2"}}}`, ExitModelIdentity},
	} {
		userConfig(t, c.body)
		t.Setenv(detachedEnvKey, "")
		parentRC, parentErr := launch()
		t.Setenv(detachedEnvKey, "1")
		childRC, childErr := child()
		if parentRC != c.rc || childRC != c.rc || parentErr != childErr {
			t.Fatalf("config %s: parent rc=%d child rc=%d (want both %d)\nparent: %s\nchild:  %s", c.body, parentRC, childRC, c.rc, parentErr, childErr)
		}
	}
	if n := registered(); n != 0 {
		t.Fatalf("a refused child registered %d round(s)", n)
	}
}

// The config file can be hand-edited past `outsource config set`, so the
// launcher applies the same defaultModel rule (config.CheckDefaultModel, with
// the row's real qualifier) and refuses with the file and the key named.
// FAIL-first: without the CheckDefaultModel call the zen row launches
// (ExitHarnessMissing) and its harness would be handed
// opencode/opencode/step-5-preview-free.
func TestUserConfigQualifiedDefaultRefusedByTheLauncher(t *testing.T) {
	launch, _, registered := cfgDetachRig(t)
	cases := []struct {
		provider, model string
		wants           []string
	}{
		{"zen", "opencode/step-5-preview-free", []string{"opencode/opencode/step-5-preview-free"}},
		{"openrouter", "openrouter/auto", []string{"router"}},
		{"openrouter", "openrouter/free", []string{"router"}},
		{"zai", "zai/glm-5.3", []string{"zai/zai/glm-5.3"}},
		{"zai", "", []string{"must not be empty"}},
		{"zai", "glm 5.3", []string{"whitespace"}},
	}
	for _, c := range cases {
		path := userConfig(t, `{"providers": {"`+c.provider+`": {"defaultModel": "`+c.model+`"}}}`)
		rc, stderr := launch("--provider", c.provider)
		if rc != ExitUsage {
			t.Fatalf("%s defaultModel %q: rc=%d, want %d; stderr=%s", c.provider, c.model, rc, ExitUsage, stderr)
		}
		mustContain(t, c.provider+" refusal", stderr, append([]string{path, "providers." + c.provider + ".defaultModel"}, c.wants...)...)
	}
	if n := registered(); n != 0 {
		t.Fatalf("refused defaults registered %d round(s)", n)
	}
	// Boundary: the bare ids pass.
	for _, c := range []struct{ provider, model string }{
		{"zen", "step-5-preview-free"},
		{"openrouter", "nvidia/nemotron-3-ultra-550b-a55b:free"},
	} {
		userConfig(t, `{"providers": {"`+c.provider+`": {"defaultModel": "`+c.model+`"}}}`)
		if rc, stderr := launch("--provider", c.provider, "--harness", "opencode"); rc != ExitHarnessMissing {
			t.Fatalf("%s bare %q: rc=%d, want %d (launch let through); stderr=%s", c.provider, c.model, rc, ExitHarnessMissing, stderr)
		}
	}
}

// openrouter's table default is empty, so without --model the launch is
// refused asking for one — unless the user config names a default, which then
// carries the launch through every pre-flight. FAIL-first: without the config
// override the config row still exits 64 "has no default model".
func TestUserConfigOpenrouterDefaultLaunchesWithoutModel(t *testing.T) {
	launch, _, _ := cfgDetachRig(t)
	if or, _ := findProvider("openrouter"); or.defaultModel != "" {
		t.Fatalf("this test's premise is openrouter's EMPTY table default (requiredModelError's case); the row now defaults to %q — move the no-config leg to a provider whose table default is empty", or.defaultModel)
	}
	rc, stderr := launch("--provider", "openrouter", "--harness", "opencode")
	if rc != ExitUsage {
		t.Fatalf("openrouter, no config: rc=%d, want %d; stderr=%s", rc, ExitUsage, stderr)
	}
	mustContain(t, "openrouter, no config", stderr, "provider openrouter has no default model — pass --model explicitly")

	userConfig(t, `{"providers": {"openrouter": {"defaultModel": "nvidia/nemotron-3-ultra-550b-a55b:free"}}}`)
	rc, stderr = launch("--provider", "openrouter", "--harness", "opencode")
	if rc != ExitHarnessMissing {
		t.Fatalf("openrouter with a config default: rc=%d, want %d (every pre-flight passed); stderr=%s", rc, ExitHarnessMissing, stderr)
	}
	mustNotContain(t, "openrouter with a config default", stderr, "has no default model")
}

// A config that does not parse refuses every launch, naming the file — a
// skipped config would route rounds the user said not to. Unknown keys, on
// the other hand, are ignored by the launcher. FAIL-first: a launcher that
// ignored the Load error would let the broken files through
// (ExitHarnessMissing).
func TestUserConfigUnparseableRefusesTheLaunch(t *testing.T) {
	launch, _, registered := cfgDetachRig(t)
	for _, c := range []struct{ body, want string }{
		{`{"providers": {"zai": `, "does not parse"},
		{`{"providers": {"zai": {"enabled": "no"}}}`, "providers.zai.enabled must be true or false"},
		{`{"providers": {"zai": {"enabled": null}}}`, "providers.zai.enabled must be true or false"},
		{`{"providers": {"zai": {"defaultModel": 5}}}`, "providers.zai.defaultModel must be a string"},
		{`["not", "an", "object"]`, "does not parse"},
	} {
		path := userConfig(t, c.body)
		rc, stderr := launch()
		if rc != ExitUsage {
			t.Fatalf("config %s: rc=%d, want %d; stderr=%s", c.body, rc, ExitUsage, stderr)
		}
		mustContain(t, "refusal for "+c.body, stderr, "refusing to launch", path, c.want)
	}
	if n := registered(); n != 0 {
		t.Fatalf("an unparseable config registered %d round(s)", n)
	}
	// Boundary: unknown keys — a future top-level section, an unknown field
	// on zai, a provider this launcher does not route (disabled, even) —
	// change nothing.
	userConfig(t, `{"future": {"x": 1}, "providers": {"ghost": {"enabled": false}, "zai": {"later": [1, 2]}}}`)
	if rc, stderr := launch(); rc != ExitHarnessMissing {
		t.Fatalf("unknown keys only: rc=%d, want %d; stderr=%s", rc, ExitHarnessMissing, stderr)
	}
}

// --list-wiring is the table when there is no config, and the table plus one
// CONFIG line per provider the config changes when there is. FAIL-first:
// without the append the CONFIG lines are missing; with an unconditional
// one, the no-op config prints lines it should not.
func TestListWiringNamesTheUserConfig(t *testing.T) {
	bare := renderWiring(providerTable, modelTable)
	wiring := func() string {
		var out bytes.Buffer
		if rc := OutsourceMain([]string{"--list-wiring"}, &out, io.Discard); rc != 0 {
			t.Fatalf("--list-wiring rc=%d", rc)
		}
		return out.String()
	}
	if got := wiring(); got != bare {
		t.Fatalf("no config: --list-wiring must be the bare matrix; got the extra:\n%s", strings.TrimPrefix(got, bare))
	}
	// A config that restates the table changes nothing.
	userConfig(t, `{"providers": {"zai": {"enabled": true, "defaultModel": "glm-5.3"}}}`)
	if got := wiring(); got != bare {
		t.Fatalf("a no-op config printed:\n%s", strings.TrimPrefix(got, bare))
	}
	// The (table: …) cells come from the rows, so a row whose default changes
	// (zen's is scheduled to empty) does not have to touch this test.
	table := func(name string) string { p, _ := findProvider(name); return orDefault(p.defaultModel, "none") }
	path := userConfig(t, `{"providers": {"muse": {"enabled": false}, "openrouter": {"defaultModel": "probe-vendor/config-default"}, "zen": {"defaultModel": "config-default-probe"}}}`)
	want := bare +
		"CONFIG " + path + ": openrouter default model probe-vendor/config-default (table: " + table("openrouter") + ")\n" +
		"CONFIG " + path + ": zen default model config-default-probe (table: " + table("zen") + ")\n" +
		"CONFIG " + path + ": muse disabled\n"
	if got := wiring(); got != want {
		t.Fatalf("CONFIG lines:\n%s\nwant:\n%s", strings.TrimPrefix(got, bare), strings.TrimPrefix(want, bare))
	}
	path = userConfig(t, `{"providers": `)
	mustContain(t, "unparseable config", strings.TrimPrefix(wiring(), bare), "CONFIG "+path+": every launch refuses — ")
}

// The exports the config CLI is injected with are the table's own answers:
// ProviderNames is providerNameList, and ProviderQualifiers is qualifierOf for
// every row — zen's is opencode, the CLI id, not the launcher name.
func TestConfigInjectionExportsAreTheTable(t *testing.T) {
	if got, want := strings.Join(ProviderNames(), " "), strings.Join(providerNameList(), " "); got != want {
		t.Fatalf("ProviderNames() = %s, want %s", got, want)
	}
	q := ProviderQualifiers()
	if len(q) != len(providerTable) {
		t.Fatalf("ProviderQualifiers has %d entries, want one per row (%d)", len(q), len(providerTable))
	}
	for _, p := range providerTable {
		if q[p.name] != qualifierOf(p) {
			t.Fatalf("ProviderQualifiers()[%s] = %q, want %q", p.name, q[p.name], qualifierOf(p))
		}
	}
	if q["zen"] != "opencode" {
		t.Fatalf("zen's qualifier = %q, want opencode", q["zen"])
	}
}
