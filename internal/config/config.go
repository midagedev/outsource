// Package config is the single owner of the user's own choices — the one
// structured config file this skill reads, config.json under the user's
// config home.
//
// outsource is shipped to other people, so a user's choices must not live in
// the shipped code. Until this package the only per-user knobs were
// environment variables (GLM_DELEGATE_MODEL) and a prose overlay; this is the
// first structured owner. A setup pane writes this file through the same CLI
// (`outsource config`), and the launcher's free-model resolver (`--model
// free`, internal/launch/free.go) reads the `free.*` keys — so the format below
// is a public contract, not a private detail.
//
// Version 1:
//
//	{
//	  "providers": {
//	    "openrouter": { "defaultModel": "nvidia/nemotron-3-ultra-550b-a55b:free" },
//	    "muse":       { "enabled": false }
//	  },
//	  "free": {
//	    "allowTraining": false,
//	    "denyPaths": ["~/work/**"]
//	  },
//	  "context": {
//	    "autoCompactWindow": 600000
//	  }
//	}
//
// The keys:
//
//   - providers.<name>.defaultModel — a BARE model id, no provider qualifier;
//     the launcher qualifies it where a harness needs that form. On a
//     catalogue provider it is also what `--model free` picks first when it
//     qualifies.
//   - providers.<name>.enabled — true or false; absent means enabled.
//   - free.allowTraining (true or false, absent = false) — when true, `--model
//     free` may pick an id whose data policy is trains, retains or unknown;
//     when false only no-train-no-retain ids qualify.
//   - free.denyPaths (an array of glob strings, `~` allowed, `**` any depth) —
//     a round whose --cwd matches one is refused `--model free`, any named id
//     the catalogue lists as free, and any named id on a catalogue provider
//     while the catalogue cannot answer (it cannot tell whether that id is
//     free).
//   - context.autoCompactWindow — a positive whole number of tokens, absent =
//     DefaultAutoCompactWindow. The launcher sets the claude-code harness's
//     CLAUDE_CODE_AUTO_COMPACT_WINDOW to it when it is below the round's
//     context window.
//
// Unknown keys anywhere are kept on write and listed as unknown by the CLI;
// the launcher ignores them. A missing file is an empty config, not an error.
// An unparseable file — bad JSON, or a known key of the wrong type — is an
// error everywhere: the launcher refuses to launch rather than route a round
// past a choice it could not read.
//
// The provider name list and the name→qualifier mapping are INJECTED by
// cmd/outsource (KnownProviders, QualifierFor): internal/launch owns them and
// imports this package, so importing launch from here would be a cycle.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Exit codes for the config CLI: usage errors, then I/O and parse failures.
const (
	ExitOK    = 0
	ExitIO    = 1
	ExitUsage = 64
)

// KnownProviders lists the provider names the CLI accepts under
// providers.<name>. Injected by cmd/outsource from internal/launch's table;
// nil means the injection is missing, which the CLI reports instead of
// guessing.
var KnownProviders func() []string

// QualifierFor maps a provider name to the qualifier a qualifying harness
// writes before its model ids (zen's is "opencode", not "zen"). Injected the
// same way; defaultModel validation turns on it.
var QualifierFor func(provider string) string

// ResolvePath answers which file holds the user's choices and which rule chose
// it: $OUTSOURCE_CONFIG (an explicit file path), else
// $XDG_CONFIG_HOME/outsource/config.json, else ~/.config/outsource/config.json.
// An empty variable counts as unset.
//
// Spelled out rather than os.UserConfigDir, which on macOS answers
// ~/Library/Application Support — not where this repo keeps its user state;
// internal/cred resolves $XDG_CONFIG_HOME/outsource/credentials the same way
// (cred.go resolvePaths). A relative OUTSOURCE_CONFIG is made absolute so
// every message names one file.
func ResolvePath() (path, source string) {
	if p := os.Getenv("OUTSOURCE_CONFIG"); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return p, "OUTSOURCE_CONFIG"
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "outsource", "config.json"), "XDG_CONFIG_HOME"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "outsource", "config.json"), "default"
}

// The known fields, in the order a write lays them out.
var (
	providerFields = []string{"defaultModel", "enabled"}
	freeFields     = []string{"allowTraining", "denyPaths"}
	contextFields  = []string{"autoCompactWindow"}
)

// DefaultAutoCompactWindow is context.autoCompactWindow when the file does not
// set it. Few models stay coherent near a million tokens of context, and the
// project owner's rule (2026-10-09) caps the auto-compact value at 600000
// whatever the window — so the shipped default caps what the launcher hands
// the claude-code harness as CLAUDE_CODE_AUTO_COMPACT_WINDOW; a window already
// below it gets nothing new.
const DefaultAutoCompactWindow = 600000

// Config is one loaded config file. Every value is kept as the raw JSON the
// file carried, so a write never drops or rewrites a key this version does
// not know; known fields are type-checked at load and read through the
// accessors below.
type Config struct {
	// Path is the file this config was loaded from and a write goes to.
	Path string

	providers map[string]map[string]json.RawMessage // name → field → raw value
	free      map[string]json.RawMessage
	context   map[string]json.RawMessage
	extra     map[string]json.RawMessage // top-level keys other than providers, free and context
}

func emptyConfig(path string) *Config {
	return &Config{
		Path:      path,
		providers: map[string]map[string]json.RawMessage{},
		free:      map[string]json.RawMessage{},
		context:   map[string]json.RawMessage{},
		extra:     map[string]json.RawMessage{},
	}
}

// Load reads the config at ResolvePath's path.
func Load() (*Config, error) {
	path, _ := ResolvePath()
	return loadFile(path)
}

// loadFile is Load at a given path. A missing (or whitespace-only) file is an
// empty config and no error — the shipped code runs unchanged for a user who
// never wrote one. Anything else that cannot be read as this format is an
// error naming the path.
func loadFile(path string) (*Config, error) {
	c := emptyConfig(path)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the config at %s: %v", path, err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return c, nil
	}
	top, err := object(b)
	if err != nil {
		return nil, fmt.Errorf("the config at %s does not parse: %v", path, err)
	}
	bad := func(key, want string) error {
		return fmt.Errorf("the config at %s does not parse: %s must be %s", path, key, want)
	}
	for k, v := range top {
		switch k {
		case "providers":
			provs, err := object(v)
			if err != nil {
				return nil, bad("providers", "an object of provider names")
			}
			for name, raw := range provs {
				fields, err := object(raw)
				if err != nil {
					return nil, bad("providers."+name, "an object")
				}
				for f, fv := range fields {
					if msg := checkField(f, fv); msg != "" {
						return nil, bad("providers."+name+"."+f, msg)
					}
				}
				c.providers[name] = fields
			}
		case "free":
			fields, err := object(v)
			if err != nil {
				return nil, bad("free", "an object")
			}
			for f, fv := range fields {
				if msg := checkField(f, fv); msg != "" {
					return nil, bad("free."+f, msg)
				}
			}
			c.free = fields
		case "context":
			fields, err := object(v)
			if err != nil {
				return nil, bad("context", "an object")
			}
			for f, fv := range fields {
				if msg := checkField(f, fv); msg != "" {
					return nil, bad("context."+f, msg)
				}
			}
			c.context = fields
		default:
			c.extra[k] = v
		}
	}
	return c, nil
}

// object decodes one JSON object. JSON null decodes into a Go map without an
// error, so it is refused by hand: null is not an object of this format.
func object(raw []byte) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("null is not an object")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// checkField is the type rule for a known field name, and the empty string for
// a valid value or an unknown name (unknown fields are kept, not judged). It
// is the one owner of "what a stored value may be": load applies it to the
// file, and the CLI's set applies it to the value it is about to write.
func checkField(field string, raw json.RawMessage) string {
	switch field {
	case "enabled", "allowTraining":
		if _, ok := boolValue(raw); !ok {
			return "true or false"
		}
	case "defaultModel":
		var s string
		if !isString(raw) || json.Unmarshal(raw, &s) != nil {
			return "a string (a bare model id)"
		}
	case "denyPaths":
		if _, msg := denyPathsValue(raw); msg != "" {
			return msg
		}
	case "autoCompactWindow":
		if _, ok := positiveInt(raw); !ok {
			return "a positive whole number of tokens, e.g. 600000"
		}
	}
	return ""
}

// positiveInt reads a JSON number written as digits only — 600000, not
// 6e5, 600000.0 or "600k" — and greater than zero. Digits only because the
// value is a token count: a fraction is meaningless, and an exponent form
// would be the one spelling a reader of the file could misread. A leading
// zero is refused too: 0600000 is not JSON, and `config set` would otherwise
// hand the writer a file it cannot render.
func positiveInt(raw json.RawMessage) (int, bool) {
	t := string(bytes.TrimSpace(raw))
	if t == "" || t[0] == '0' || strings.Trim(t, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(t)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// boolValue reads a JSON boolean. json.Unmarshal accepts null into a bool as
// false without an error — which would turn `"enabled": null` into a disabled
// provider — so the literal is matched instead.
func boolValue(raw json.RawMessage) (value, ok bool) {
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

func isString(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '"'
}

// denyPathsValue reads free.denyPaths: an array of non-empty glob strings. A
// null array, or a null element, decodes without error in Go and is refused
// here; each glob must also be well-formed (filepath.Match syntax, which is
// checked over the whole pattern). `~` is stored as written — expanding it is
// the reader's job.
func denyPathsValue(raw json.RawMessage) ([]string, string) {
	const want = "an array of glob strings, e.g. [\"~/work/**\"]"
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '[' {
		return nil, want
	}
	var v []string
	if err := json.Unmarshal(t, &v); err != nil {
		return nil, want
	}
	for _, g := range v {
		if g == "" {
			return nil, want + " (an empty or null entry is not a glob)"
		}
		if _, err := filepath.Match(g, ""); err != nil {
			return nil, fmt.Sprintf("%s (%q is not a well-formed glob)", want, g)
		}
	}
	return v, ""
}

// Enabled answers providers.<name>.enabled. Absent means enabled: present is
// true only when the file has the key.
func (c *Config) Enabled(provider string) (value, present bool) {
	raw, ok := c.providers[provider]["enabled"]
	if !ok {
		return true, false
	}
	v, _ := boolValue(raw) // load refused any other shape
	return v, true
}

// DefaultModel answers providers.<name>.defaultModel. present with an empty
// model means the file holds "" — CheckDefaultModel refuses that, it is not
// the same as unset.
func (c *Config) DefaultModel(provider string) (model string, present bool) {
	raw, ok := c.providers[provider]["defaultModel"]
	if !ok {
		return "", false
	}
	json.Unmarshal(raw, &model) // load refused any other shape
	return model, true
}

// AllowTraining answers free.allowTraining (absent = false). The launcher's
// free-model resolver reads it.
func (c *Config) AllowTraining() (value, present bool) {
	raw, ok := c.free["allowTraining"]
	if !ok {
		return false, false
	}
	v, _ := boolValue(raw)
	return v, true
}

// DenyPaths answers free.denyPaths, the globs as written (`~` unexpanded).
// The launcher's free-model resolver and its named-id pre-flight read it.
func (c *Config) DenyPaths() (paths []string, present bool) {
	raw, ok := c.free["denyPaths"]
	if !ok {
		return nil, false
	}
	v, _ := denyPathsValue(raw)
	return v, true
}

// AutoCompactWindow answers context.autoCompactWindow: the file's value, or
// DefaultAutoCompactWindow when the file does not set it (present false).
func (c *Config) AutoCompactWindow() (value int, present bool) {
	raw, ok := c.context["autoCompactWindow"]
	if !ok {
		return DefaultAutoCompactWindow, false
	}
	v, _ := positiveInt(raw) // load refused any other shape
	return v, true
}

// UnknownKeys lists, sorted, every dotted key in the file this version does
// not know. knownProviders is the injected name list: a providers.<name> whose
// name is not in it is unknown whole, because the launcher can never read a
// provider it does not route.
func (c *Config) UnknownKeys(knownProviders []string) []string {
	known := map[string]bool{}
	for _, n := range knownProviders {
		known[n] = true
	}
	out := []string{}
	for k := range c.extra {
		out = append(out, k)
	}
	for name, fields := range c.providers {
		if !known[name] {
			out = append(out, "providers."+name)
			continue
		}
		for f := range fields {
			if !contains(providerFields, f) {
				out = append(out, "providers."+name+"."+f)
			}
		}
	}
	for f := range c.free {
		if !contains(freeFields, f) {
			out = append(out, "free."+f)
		}
	}
	for f := range c.context {
		if !contains(contextFields, f) {
			out = append(out, "context."+f)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// section is the flat field map a free.* or context.* key lives in.
func (c *Config) section(k key) map[string]json.RawMessage {
	if k.section == sectionContext {
		return c.context
	}
	return c.free
}

// set stores one known key's raw value; the caller validated it.
func (c *Config) set(k key, raw json.RawMessage) {
	if k.section != sectionProviders {
		c.section(k)[k.field] = raw
		return
	}
	if c.providers[k.provider] == nil {
		c.providers[k.provider] = map[string]json.RawMessage{}
	}
	c.providers[k.provider][k.field] = raw
}

// unset removes one known key and reports whether it was there. A
// providers.<name> object left empty goes with it, so unsetting everything
// converges on {} instead of accreting empty objects.
func (c *Config) unset(k key) bool {
	if k.section != sectionProviders {
		m := c.section(k)
		_, had := m[k.field]
		delete(m, k.field)
		return had
	}
	fields := c.providers[k.provider]
	_, had := fields[k.field]
	delete(fields, k.field)
	if had && len(fields) == 0 {
		delete(c.providers, k.provider)
	}
	return had
}

// render lays the config out in a stable order: providers (names sorted, the
// known fields first, then unknown ones sorted), free (likewise), context
// (likewise), then unknown top-level keys sorted; an empty object is left
// out. Raw values are embedded as the file had them and json.Indent only
// re-flows whitespace, so an unknown value keeps its own key order and every
// token byte.
func (c *Config) render() ([]byte, error) {
	var b bytes.Buffer
	writeObject(&b, func(member func(string, []byte)) {
		if len(c.providers) > 0 {
			var pb bytes.Buffer
			writeObject(&pb, func(pm func(string, []byte)) {
				for _, name := range sortedNames(c.providers) {
					var eb bytes.Buffer
					writeFields(&eb, c.providers[name], providerFields)
					pm(name, eb.Bytes())
				}
			})
			member("providers", pb.Bytes())
		}
		if len(c.free) > 0 {
			var fb bytes.Buffer
			writeFields(&fb, c.free, freeFields)
			member("free", fb.Bytes())
		}
		if len(c.context) > 0 {
			var cb bytes.Buffer
			writeFields(&cb, c.context, contextFields)
			member("context", cb.Bytes())
		}
		for _, k := range sortedNames(c.extra) {
			member(k, c.extra[k])
		}
	})
	var out bytes.Buffer
	if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// writeObject writes {"k":v,...} with the members its body emits, in order.
func writeObject(b *bytes.Buffer, body func(member func(k string, v []byte))) {
	b.WriteByte('{')
	first := true
	body(func(k string, v []byte) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(encode(k))
		b.WriteByte(':')
		b.Write(bytes.TrimSpace(v))
	})
	b.WriteByte('}')
}

// writeFields writes one object: the known fields in their order, then the
// rest sorted.
func writeFields(b *bytes.Buffer, fields map[string]json.RawMessage, known []string) {
	writeObject(b, func(member func(string, []byte)) {
		for _, f := range known {
			if v, ok := fields[f]; ok {
				member(f, v)
			}
		}
		for _, f := range sortedNames(fields) {
			if !contains(known, f) {
				member(f, fields[f])
			}
		}
	})
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// encode marshals v as JSON without HTML escaping, so a key or glob holding <,
// > or & is written (and printed) as the user wrote it rather than as \u003c.
func encode(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("null") // unreachable for the types this package passes
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// renameHook is the indirection write's final rename goes through, so a test
// can make the rename fail and assert that no partial file is left behind.
// "Atomic" is a claim about the failure path, which no ordinary run takes.
var renameHook = os.Rename

// write stores the config atomically: a temp file in the target's own
// directory (created 0755 if missing), synced, mode 0644, then one rename —
// a reader or a crash sees the old file or the new one, never half of one.
//
// A config that is a symlink (a dotfiles checkout) is written through: the
// rename lands on the file the link points at, so the link survives. Renaming
// onto the link itself would replace it with a regular file and silently fork
// the user's dotfiles.
func (c *Config) write() error {
	data, err := c.render()
	if err != nil {
		return fmt.Errorf("cannot render the config for %s: %v", c.Path, err)
	}
	target := c.Path
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if real, err := filepath.EvalSymlinks(target); err == nil {
			target = real
		}
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create the config directory %s: %v", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config.json.tmp-*")
	if err != nil {
		return fmt.Errorf("cannot write the config at %s: %v", c.Path, err)
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename has happened
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameHook(tmp.Name(), target)
	}
	if err != nil {
		return fmt.Errorf("cannot write the config at %s: %v", c.Path, err)
	}
	return nil
}

// CheckDefaultModel refuses a defaultModel that must not be stored, and is
// the ONE owner of that rule: the CLI's set and the launcher's load-time check
// both call it, so a hand-edited file meets the same refusal as a typo on the
// command line. qualifier is a parameter because the two callers reach it by
// different roads — the launcher from its provider row (qualifierOf), the CLI
// from the injected table. The empty string means the value is acceptable.
//
//   - not empty, and no whitespace: one bare id.
//   - not <qualifier>/<id>: the harness qualifies the id itself, so a stored
//     opencode/step-5-preview-free on zen would reach the opencode CLI as
//     opencode/opencode/step-5-preview-free.
//     On openrouter the bare ids are <vendor>/<id> and openrouter is itself a
//     vendor, so the qualified form is openrouter/<vendor>/<id>: a further
//     slash, not the prefix alone — the rule internal/launch's normalizeModel
//     applies to --model (TestOpenrouterQualifiedFormHasOneAnswer holds the
//     two to one answer).
//   - on openrouter, OpenRouter's own router ids (openrouter/auto,
//     openrouter/free) are refused, and the message says why: a router picks
//     a different model per request, so the launcher's model-identity check
//     cannot pin it, and a default it cannot verify is not a default.
func CheckDefaultModel(provider, qualifier, model string) string {
	if model == "" {
		return fmt.Sprintf("a default model must not be empty — remove it with: outsource config unset providers.%s.defaultModel", provider)
	}
	if strings.ContainsAny(model, " \t\r\n") {
		return fmt.Sprintf("a default model is one bare id with no whitespace, got %q", model)
	}
	if provider == "openrouter" && (model == "openrouter/auto" || model == "openrouter/free") {
		return fmt.Sprintf("%s is one of OpenRouter's own router ids: a router picks a different model per request, so the launcher's model-identity check cannot pin it and it cannot be a default — name one concrete model id", model)
	}
	if rest, ok := strings.CutPrefix(model, qualifier+"/"); qualifier != "" && ok && (provider != "openrouter" || strings.Contains(rest, "/")) {
		return fmt.Sprintf("a default model is stored bare and the launcher adds the %s/ qualifier itself, so %q would reach the harness as %s/%s — store %q", qualifier, model, qualifier, model, rest)
	}
	return ""
}
