package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestMain keeps every test in this package off the developer's real config:
// OUTSOURCE_CONFIG points at a file that does not exist and HOME at a private
// directory, so a test that forgets its own t.Setenv reads an empty config and
// writes nowhere real (this round must never create ~/.config/outsource/).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "outsource-config-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "config tests: cannot create a private dir:", err)
		os.Exit(2)
	}
	os.Setenv("HOME", filepath.Join(dir, "home"))
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Setenv("OUTSOURCE_CONFIG", filepath.Join(dir, "absent", "config.json"))
	rc := m.Run()
	os.RemoveAll(dir)
	os.Exit(rc)
}

// stubTable injects a provider table shaped like the launcher's (zen's
// qualifier is opencode, the others their own name). The real table's
// mapping is pinned where it lives, in internal/launch's config tests; this
// only gives the CLI something to validate against.
func stubTable(t *testing.T) {
	t.Helper()
	oldNames, oldQual := KnownProviders, QualifierFor
	t.Cleanup(func() { KnownProviders, QualifierFor = oldNames, oldQual })
	KnownProviders = func() []string { return []string{"zai", "openrouter", "zen", "muse"} }
	QualifierFor = func(p string) string {
		if p == "zen" {
			return "opencode"
		}
		return p
	}
}

// configAt points this test's config at path (which may not exist yet).
func configAt(t *testing.T, path string) string {
	t.Helper()
	t.Setenv("OUTSOURCE_CONFIG", path)
	return path
}

// cli runs `outsource config <args>` and returns rc, stdout, stderr.
func cli(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	rc := Main(args, &out, &errb)
	return rc, out.String(), errb.String()
}

func mustRC(t *testing.T, want int, args ...string) (string, string) {
	t.Helper()
	rc, out, errb := cli(args...)
	if rc != want {
		t.Fatalf("config %s: rc=%d, want %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), rc, want, out, errb)
	}
	return out, errb
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Contract 1: OUTSOURCE_CONFIG beats XDG_CONFIG_HOME beats the default, an
// empty variable counts as unset, and `config path` names the rule that chose
// the file. FAIL-first: with the XDG branch first, the OUTSOURCE_CONFIG row
// answers /xdg/outsource/config.json.
func TestResolvePathPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		explicit, xdg, path, source string
	}{
		{"/x/c.json", "/xdg", "/x/c.json", "OUTSOURCE_CONFIG"},
		{"", "/xdg", "/xdg/outsource/config.json", "XDG_CONFIG_HOME"},
		{"", "", filepath.Join(home, ".config", "outsource", "config.json"), "default"},
	}
	for _, c := range cases {
		t.Setenv("OUTSOURCE_CONFIG", c.explicit)
		t.Setenv("XDG_CONFIG_HOME", c.xdg)
		if path, source := ResolvePath(); path != c.path || source != c.source {
			t.Fatalf("OUTSOURCE_CONFIG=%q XDG_CONFIG_HOME=%q: got %s (%s), want %s (%s)", c.explicit, c.xdg, path, source, c.path, c.source)
		}
		out, _ := mustRC(t, ExitOK, "path")
		if want := c.path + "\nsource: " + c.source + "\nexists: no\n"; out != want {
			t.Fatalf("config path = %q, want %q", out, want)
		}
	}
	// A relative OUTSOURCE_CONFIG is named absolutely, so every message names
	// one file whatever the reader's cwd.
	wd, _ := os.Getwd()
	t.Setenv("OUTSOURCE_CONFIG", "rel/c.json")
	if path, _ := ResolvePath(); path != filepath.Join(wd, "rel", "c.json") {
		t.Fatalf("relative OUTSOURCE_CONFIG resolved to %s", path)
	}
	// exists: yes once the file is there; --json carries the same three facts.
	p := configAt(t, filepath.Join(t.TempDir(), "c.json"))
	os.WriteFile(p, []byte("{}\n"), 0o644)
	out, _ := mustRC(t, ExitOK, "path", "--json")
	if want := `{"path":"` + p + `","source":"OUTSOURCE_CONFIG","exists":true}` + "\n"; out != want {
		t.Fatalf("config path --json = %q, want %q", out, want)
	}
}

// Contract 2, the load half: a missing or blank file is the empty config; a
// file that is not this format is an error naming the path. FAIL-first: the
// inherited loader decoded into a struct with unexported fields and read
// every file as empty — `"enabled": false` came back (unset).
func TestLoad(t *testing.T) {
	dir := t.TempDir()
	c, err := loadFile(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatalf("a missing file must be the empty config, got: %v", err)
	}
	if on, set := c.Enabled("zai"); !on || set {
		t.Fatalf("empty config: Enabled = %v,%v, want true (absent)", on, set)
	}
	blank := filepath.Join(dir, "blank.json")
	os.WriteFile(blank, []byte(" \n"), 0o644)
	if _, err := loadFile(blank); err != nil {
		t.Fatalf("a blank file must be the empty config, got: %v", err)
	}

	good := filepath.Join(dir, "good.json")
	os.WriteFile(good, []byte(`{"providers": {"zai": {"enabled": false, "defaultModel": "glm-4.6"}}, "free": {"allowTraining": true, "denyPaths": ["~/w/**"]}}`), 0o644)
	c, err = loadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if on, set := c.Enabled("zai"); on || !set {
		t.Fatalf("Enabled(zai) = %v,%v, want false,true", on, set)
	}
	if m, set := c.DefaultModel("zai"); m != "glm-4.6" || !set {
		t.Fatalf("DefaultModel(zai) = %q,%v", m, set)
	}
	if v, set := c.AllowTraining(); !v || !set {
		t.Fatalf("AllowTraining = %v,%v", v, set)
	}
	if v, set := c.DenyPaths(); !set || len(v) != 1 || v[0] != "~/w/**" {
		t.Fatalf("DenyPaths = %v,%v", v, set)
	}

	for _, c := range []struct{ body, want string }{
		{`{"providers": `, "does not parse"},
		{`{} trailing`, "does not parse"},
		{`[1]`, "does not parse"},
		{`null`, "does not parse"},
		{`{"providers": 3}`, "providers must be an object"},
		{`{"providers": {"zai": "off"}}`, "providers.zai must be an object"},
		{`{"providers": {"zai": {"enabled": "yes"}}}`, "providers.zai.enabled must be true or false"},
		{`{"providers": {"zai": {"enabled": null}}}`, "providers.zai.enabled must be true or false"},
		{`{"providers": {"zai": {"defaultModel": 5}}}`, "providers.zai.defaultModel must be a string"},
		{`{"free": []}`, "free must be an object"},
		{`{"free": {"allowTraining": 1}}`, "free.allowTraining must be true or false"},
		{`{"free": {"denyPaths": "~/w"}}`, "free.denyPaths must be an array of glob strings"},
		{`{"free": {"denyPaths": null}}`, "free.denyPaths must be an array of glob strings"},
		{`{"free": {"denyPaths": [null]}}`, "free.denyPaths must be an array of glob strings"},
		{`{"free": {"denyPaths": ["a["]}}`, "not a well-formed glob"},
	} {
		path := filepath.Join(dir, "bad.json")
		os.WriteFile(path, []byte(c.body), 0o644)
		_, err := loadFile(path)
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("load %s: err = %v, want one naming %s and %q", c.body, err, path, c.want)
		}
	}
}

// Contract 2, the write half: every unknown key — top level, under a known
// provider, a provider this binary does not route, under free — survives a
// set byte for byte (key, key order inside its value, number and escape
// tokens), and `list` names each as unknown (kept). FAIL-first: the inherited
// writer, given this file, wrote back only the muse entry.
func TestUnknownKeysSurviveASet(t *testing.T) {
	stubTable(t)
	path := configAt(t, filepath.Join(t.TempDir(), "config.json"))
	os.WriteFile(path, []byte(`{"x<&>": "keep me", "future": {"b": 1.50, "a": ["\u00e9t\u00e9", true]},
  "providers": {"zai": {"enabled": false, "tier": "pro"}, "ghost": {"enabled": false}},
  "free": {"later": 3}}`), 0o644)

	mustRC(t, ExitOK, "set", "providers.muse.enabled", "false")
	got := read(t, path)
	for _, want := range []string{
		`"x<&>": "keep me"`,
		`"future": {
    "b": 1.50,
    "a": [
      "\u00e9t\u00e9",
      true
    ]
  }`,
		`"tier": "pro"`,
		`"ghost": {
      "enabled": false
    }`,
		`"later": 3`,
		`"muse": {
      "enabled": false
    }`,
		`"zai": {
      "enabled": false,
      "tier": "pro"
    }`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("after set, the file lost %s:\n%s", want, got)
		}
	}
	out, _ := mustRC(t, ExitOK, "list")
	for _, k := range []string{"free.later", "future", "providers.ghost", "providers.zai.tier", "x<&>"} {
		if !strings.Contains(out, k+" = unknown (kept)\n") {
			t.Fatalf("list does not mark %s unknown (kept):\n%s", k, out)
		}
	}
	if !strings.Contains(out, "providers.zai.enabled = false\n") || !strings.Contains(out, "providers.muse.enabled = false\n") {
		t.Fatalf("list lost a known value:\n%s", out)
	}
}

// Keys land in one order whatever order they were set in, and the layout is
// the format's: providers (names sorted, known fields first), then free.
// FAIL-first: rendering map iteration order makes the two files differ.
func TestWriteIsStable(t *testing.T) {
	stubTable(t)
	sets := [][]string{
		{"providers.openrouter.defaultModel", "nvidia/nemotron-3-ultra-550b-a55b:free"},
		{"providers.muse.enabled", "false"},
		{"free.denyPaths", `["~/work/**"]`},
		{"free.allowTraining", "false"},
		{"providers.muse.defaultModel", "muse-spark-1.3-contributor"},
	}
	write := func(order []int) string {
		path := configAt(t, filepath.Join(t.TempDir(), "config.json"))
		for _, i := range order {
			mustRC(t, ExitOK, "set", sets[i][0], sets[i][1])
		}
		return read(t, path)
	}
	a := write([]int{0, 1, 2, 3, 4})
	b := write([]int{4, 3, 2, 1, 0})
	if a != b {
		t.Fatalf("set order changed the file:\n%s\n---\n%s", a, b)
	}
	want := `{
  "providers": {
    "muse": {
      "defaultModel": "muse-spark-1.3-contributor",
      "enabled": false
    },
    "openrouter": {
      "defaultModel": "nvidia/nemotron-3-ultra-550b-a55b:free"
    }
  },
  "free": {
    "allowTraining": false,
    "denyPaths": [
      "~/work/**"
    ]
  }
}
`
	if a != want {
		t.Fatalf("layout:\n%s\nwant:\n%s", a, want)
	}
}

// Writes are atomic: a temp file in the target's directory, then a rename. A
// failed rename leaves the old file intact and no temp file behind, and a
// first write that fails creates nothing. FAIL-first: a write straight to
// the path (os.WriteFile) leaves the new bytes in place when the "rename"
// step fails, and dropping the deferred Remove leaves .config.json.tmp-*.
func TestWriteIsAtomicOnRenameFailure(t *testing.T) {
	stubTable(t)
	old := renameHook
	t.Cleanup(func() { renameHook = old })
	renameHook = func(string, string) error { return errors.New("simulated rename failure") }

	dir := t.TempDir()
	path := configAt(t, filepath.Join(dir, "config.json"))
	before := `{"providers": {"zai": {"enabled": false}}}`
	os.WriteFile(path, []byte(before), 0o644)
	_, errb := mustRC(t, ExitIO, "set", "providers.muse.enabled", "false")
	if !strings.Contains(errb, "simulated rename failure") || !strings.Contains(errb, path) {
		t.Fatalf("the failure must name the file and the cause: %s", errb)
	}
	if got := read(t, path); got != before {
		t.Fatalf("a failed write changed the file:\n%s", got)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("a failed write left files behind: %v", names)
	}

	fresh := configAt(t, filepath.Join(t.TempDir(), "sub", "config.json"))
	mustRC(t, ExitIO, "set", "providers.muse.enabled", "false")
	if _, err := os.Stat(fresh); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed first write created %s", fresh)
	}
	ents, _ = os.ReadDir(filepath.Dir(fresh))
	if len(ents) != 0 {
		t.Fatalf("a failed first write left %d file(s) in %s", len(ents), filepath.Dir(fresh))
	}
}

// A first write creates the directory 0755 and the file 0644. FAIL-first:
// without the Chmod the file is CreateTemp's 0600.
func TestWriteModes(t *testing.T) {
	stubTable(t)
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	path := configAt(t, filepath.Join(t.TempDir(), "outsource", "config.json"))
	mustRC(t, ExitOK, "set", "providers.zai.enabled", "true")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("file mode %v, want 0644", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o755 {
		t.Fatalf("dir mode %v, want 0755", di.Mode().Perm())
	}
}

// A config that is a symlink (dotfiles) is written through: the link stays a
// link and the file it points at gets the new content. FAIL-first: renaming
// onto the link path replaces the link with a regular file.
func TestWriteThroughASymlink(t *testing.T) {
	stubTable(t)
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "outsource.json")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte(`{"keep": 1}`), 0o644)
	link := configAt(t, filepath.Join(dir, "config.json"))
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	mustRC(t, ExitOK, "set", "providers.muse.enabled", "false")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the config symlink was replaced by a regular file (err %v)", err)
	}
	if got := read(t, real); !strings.Contains(got, `"keep": 1`) || !strings.Contains(got, `"enabled": false`) {
		t.Fatalf("the link target did not get the write:\n%s", got)
	}
}

// Contract 6, set validation: every refusal is exit 64 with what was wrong,
// and none of them writes. FAIL-first per row: drop the matching check and
// that row exits 0 and creates the file.
func TestSetRefusals(t *testing.T) {
	stubTable(t)
	path := configAt(t, filepath.Join(t.TempDir(), "config.json"))
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"set", "providers.zai.enabled", "yes"}, "takes true or false"},
		{[]string{"set", "free.allowTraining", "False"}, "takes true or false"},
		{[]string{"set", "free.denyPaths", "~/work/**"}, "free.denyPaths takes an array of glob strings"},
		{[]string{"set", "free.denyPaths", "null"}, "free.denyPaths takes an array of glob strings"},
		{[]string{"set", "free.denyPaths", `{"a": 1}`}, "free.denyPaths takes an array of glob strings"},
		{[]string{"set", "free.denyPaths", `["~/ok", ""]`}, "empty or null entry"},
		{[]string{"set", "free.denyPaths", `["a["]`}, "not a well-formed glob"},
		{[]string{"set", "providers.nope.enabled", "false"}, "unknown provider \"nope\" in providers.nope.enabled — known providers: zai openrouter zen muse"},
		{[]string{"set", "providers.zai.color", "red"}, "unknown key \"providers.zai.color\" — valid keys: " + keyShapes},
		{[]string{"set", "zai.enabled", "false"}, "unknown key"},
		{[]string{"set", "free.other", "1"}, "unknown key"},
		{[]string{"set", "providers.zen.defaultModel", "opencode/step-5-preview-free"}, "opencode/opencode/step-5-preview-free"},
		{[]string{"set", "providers.openrouter.defaultModel", "openrouter/auto"}, "router"},
		{[]string{"set", "providers.openrouter.defaultModel", "openrouter/free"}, "router"},
		{[]string{"set", "providers.zai.defaultModel", "zai/glm-5.3"}, "stored bare"},
		{[]string{"set", "providers.zai.defaultModel", "glm 5.3"}, "whitespace"},
		{[]string{"set", "providers.zai.defaultModel", ""}, "must not be empty"},
		{[]string{"set", "providers.zai.enabled"}, "takes 2 argument(s), got 1"},
		{[]string{"get"}, "takes 1 argument(s), got 0"},
		{[]string{"frob"}, "unknown command"},
	} {
		_, errb := mustRC(t, ExitUsage, c.args...)
		if !strings.Contains(errb, c.want) {
			t.Fatalf("config %s: stderr %q, want %q", strings.Join(c.args, " "), errb, c.want)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused set created the config file")
	}
	// Boundary: the qualifier is the row's, not its name — zen's bare id and
	// an openrouter vendor/id pass.
	mustRC(t, ExitOK, "set", "providers.zen.defaultModel", "step-5-preview-free")
	mustRC(t, ExitOK, "set", "providers.openrouter.defaultModel", "nvidia/nemotron-3-ultra-550b-a55b:free")
}

// A file that does not parse is exit 1 for every reader and is never written
// over: a rewrite would drop what could not be read. FAIL-first: a set that
// ignored the load error wrote a fresh file over the broken one.
func TestUnparseableFileIsNeverWrittenOver(t *testing.T) {
	stubTable(t)
	path := configAt(t, filepath.Join(t.TempDir(), "config.json"))
	broken := `{"providers": {"zai": {"enabled": false},`
	os.WriteFile(path, []byte(broken), 0o644)
	for _, args := range [][]string{
		{"set", "providers.muse.enabled", "false"},
		{"unset", "providers.zai.enabled"},
		{"list"},
		{"get", "providers.zai.enabled"},
	} {
		_, errb := mustRC(t, ExitIO, args...)
		if !strings.Contains(errb, path) || !strings.Contains(errb, "does not parse") {
			t.Fatalf("config %s: stderr %q must name the file and say it does not parse", strings.Join(args, " "), errb)
		}
	}
	if got := read(t, path); got != broken {
		t.Fatalf("the broken file was rewritten:\n%s", got)
	}
}

// The read verbs on every known key, and unset converging back to {}.
func TestGetListUnset(t *testing.T) {
	stubTable(t)
	path := configAt(t, filepath.Join(t.TempDir(), "config.json"))

	// unset on a missing file writes nothing.
	mustRC(t, ExitOK, "unset", "providers.zai.enabled")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unset on a missing file created it")
	}

	mustRC(t, ExitOK, "set", "providers.muse.enabled", "false")
	mustRC(t, ExitOK, "set", "free.denyPaths", `["~/work/**", "/srv/<&>"]`)
	for _, c := range []struct{ args, want string }{
		{"get providers.muse.enabled", "false\n"},
		{"get providers.muse.enabled --json", "false\n"},
		{"get providers.zai.enabled", "(unset)\n"},
		{"get providers.zai.enabled --json", "null\n"},
		{"get free.denyPaths --json", `["~/work/**","/srv/<&>"]` + "\n"},
		{"get free.allowTraining", "(unset)\n"},
	} {
		out, _ := mustRC(t, ExitOK, strings.Fields(c.args)...)
		if out != c.want {
			t.Fatalf("config %s = %q, want %q", c.args, out, c.want)
		}
	}

	// 2026-10-09 (round free): the format gained context.autoCompactWindow,
	// and `list` prints every known key — so the listing ends with its line
	// and --json carries 11 values: the stub table's 4 providers × 2 fields,
	// 2 free fields, 1 context field. Contract unchanged: every known key,
	// unset ones as (unset) / null. FAIL-first: dropping contextFields from
	// allKeys makes this listing lack the line and the count read 10.
	out, _ := mustRC(t, ExitOK, "list")
	wantList := "# " + path + "\n" +
		"providers.zai.defaultModel = (unset)\nproviders.zai.enabled = (unset)\n" +
		"providers.openrouter.defaultModel = (unset)\nproviders.openrouter.enabled = (unset)\n" +
		"providers.zen.defaultModel = (unset)\nproviders.zen.enabled = (unset)\n" +
		"providers.muse.defaultModel = (unset)\nproviders.muse.enabled = false\n" +
		"free.allowTraining = (unset)\n" + `free.denyPaths = ["~/work/**","/srv/<&>"]` + "\n" +
		"context.autoCompactWindow = (unset)\n"
	if out != wantList {
		t.Fatalf("list:\n%s\nwant:\n%s", out, wantList)
	}

	out, _ = mustRC(t, ExitOK, "list", "--json")
	var doc struct {
		Path    string         `json:"path"`
		Values  map[string]any `json:"values"`
		Unknown []string       `json:"unknown"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("list --json is not JSON: %v\n%s", err, out)
	}
	if doc.Path != path || len(doc.Values) != 11 || doc.Values["providers.muse.enabled"] != false {
		t.Fatalf("list --json: %+v", doc)
	}
	if v, ok := doc.Values["providers.zai.enabled"]; !ok || v != nil {
		t.Fatalf("an unset key must be present as null, got %v (present %v)", v, ok)
	}
	if !strings.Contains(out, `"unknown":[]`) {
		t.Fatalf("no unknown keys must be [], not null: %s", out)
	}

	mustRC(t, ExitOK, "unset", "providers.muse.enabled")
	if strings.Contains(read(t, path), "muse") {
		t.Fatalf("an emptied provider entry must go with its last key:\n%s", read(t, path))
	}
	mustRC(t, ExitOK, "unset", "free.denyPaths")
	if got := read(t, path); got != "{}\n" {
		t.Fatalf("unsetting every key must leave {}, got %q", got)
	}
}

// A binary that forgot the injection says so instead of listing no providers.
func TestMissingInjectionIsReported(t *testing.T) {
	oldNames, oldQual := KnownProviders, QualifierFor
	t.Cleanup(func() { KnownProviders, QualifierFor = oldNames, oldQual })
	KnownProviders, QualifierFor = nil, nil
	configAt(t, filepath.Join(t.TempDir(), "config.json"))
	for _, args := range [][]string{{"list"}, {"set", "providers.zai.enabled", "true"}} {
		_, errb := mustRC(t, ExitUsage, args...)
		if !strings.Contains(errb, "not injected") {
			t.Fatalf("config %s: %q", strings.Join(args, " "), errb)
		}
	}
	// free.* needs no table.
	mustRC(t, ExitOK, "set", "free.allowTraining", "true")
}

// context.autoCompactWindow: a positive whole number of tokens, absent = the
// shipped default. Every other shape is a load error like any bad type, and
// set refuses the same shapes before it writes. FAIL-first: with the
// autoCompactWindow case removed from checkField, `0`, `-1` and `"600k"` load
// without error and AutoCompactWindow answers 0.
func TestAutoCompactWindow(t *testing.T) {
	stubTable(t)
	dir := t.TempDir()
	c, err := loadFile(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if v, set := c.AutoCompactWindow(); v != DefaultAutoCompactWindow || set || DefaultAutoCompactWindow != 600000 {
		t.Fatalf("absent: AutoCompactWindow = %d,%v, want %d (the shipped 600000),false", v, set, DefaultAutoCompactWindow)
	}
	good := filepath.Join(dir, "good.json")
	os.WriteFile(good, []byte(`{"context": {"autoCompactWindow": 200000}}`), 0o644)
	if c, err = loadFile(good); err != nil {
		t.Fatal(err)
	}
	if v, set := c.AutoCompactWindow(); v != 200000 || !set {
		t.Fatalf("AutoCompactWindow = %d,%v, want 200000,true", v, set)
	}
	for _, body := range []string{
		`{"context": {"autoCompactWindow": 0}}`,
		`{"context": {"autoCompactWindow": -1}}`,
		`{"context": {"autoCompactWindow": "600k"}}`,
		`{"context": {"autoCompactWindow": "600000"}}`,
		`{"context": {"autoCompactWindow": 600000.5}}`,
		`{"context": {"autoCompactWindow": 6e5}}`,
		`{"context": {"autoCompactWindow": null}}`,
	} {
		path := filepath.Join(dir, "bad.json")
		os.WriteFile(path, []byte(body), 0o644)
		_, err := loadFile(path)
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "context.autoCompactWindow must be a positive whole number of tokens") {
			t.Fatalf("load %s: err = %v, want one naming %s and the key's rule", body, err, path)
		}
	}
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte(`{"context": []}`), 0o644)
	if _, err := loadFile(path); err == nil || !strings.Contains(err.Error(), "context must be an object") {
		t.Fatalf(`{"context": []}: err = %v`, err)
	}

	// The CLI: set refuses the same shapes and writes nothing, then the good
	// value round-trips through get, list and unset, and lands after free.
	cfgPath := configAt(t, filepath.Join(t.TempDir(), "config.json"))
	for _, v := range []string{"0", "-1", "600k", "0600000", "1.5", "1e6", ""} {
		_, errb := mustRC(t, ExitUsage, "set", "context.autoCompactWindow", v)
		if !strings.Contains(errb, "context.autoCompactWindow takes a positive whole number of tokens") {
			t.Fatalf("set %q: stderr %q", v, errb)
		}
	}
	if _, err := os.Stat(cfgPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused set created the config file")
	}
	mustRC(t, ExitOK, "set", "free.allowTraining", "true")
	out, _ := mustRC(t, ExitOK, "set", "context.autoCompactWindow", "400000")
	if want := "context.autoCompactWindow = 400000 (" + cfgPath + ")\n"; out != want {
		t.Fatalf("set: %q, want %q", out, want)
	}
	if got, want := read(t, cfgPath), "{\n  \"free\": {\n    \"allowTraining\": true\n  },\n  \"context\": {\n    \"autoCompactWindow\": 400000\n  }\n}\n"; got != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
	for _, c := range []struct{ args, want string }{
		{"get context.autoCompactWindow", "400000\n"},
		{"get context.autoCompactWindow --json", "400000\n"},
	} {
		if out, _ := mustRC(t, ExitOK, strings.Fields(c.args)...); out != c.want {
			t.Fatalf("config %s = %q, want %q", c.args, out, c.want)
		}
	}
	if out, _ := mustRC(t, ExitOK, "list"); !strings.Contains(out, "\ncontext.autoCompactWindow = 400000\n") {
		t.Fatalf("list does not show the cap:\n%s", out)
	}
	mustRC(t, ExitOK, "unset", "context.autoCompactWindow")
	if out, _ := mustRC(t, ExitOK, "get", "context.autoCompactWindow"); out != "(unset)\n" {
		t.Fatalf("after unset: %q", out)
	}
	if strings.Contains(read(t, cfgPath), "context") {
		t.Fatalf("an emptied context object must go with its last key:\n%s", read(t, cfgPath))
	}
	// An unknown field under context is kept and listed as unknown, like
	// every other section's.
	os.WriteFile(cfgPath, []byte(`{"context": {"later": 1}}`), 0o644)
	if out, _ := mustRC(t, ExitOK, "list"); !strings.Contains(out, "context.later = unknown (kept)\n") {
		t.Fatalf("list does not keep context.later:\n%s", out)
	}
	// The usage names the key and the default the launcher applies.
	_, errb := mustRC(t, ExitUsage, "set", "context.other", "1")
	if !strings.Contains(errb, "context.autoCompactWindow") {
		t.Fatalf("the key shapes must name context.autoCompactWindow: %s", errb)
	}
	if out, _ := mustRC(t, ExitOK, "help"); !strings.Contains(out, "(unset: 600000)") {
		t.Fatalf("usage must name the default:\n%s", out)
	}
	// context.* needs no provider table, like free.*.
	KnownProviders, QualifierFor = nil, nil
	mustRC(t, ExitOK, "set", "context.autoCompactWindow", "200000")
}
