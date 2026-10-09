// The `outsource config` tool: read and write the user's own choices in the
// one config file this package owns. The verbs are deliberately few — path
// (which file, and what chose it), list (every known key, plus the unknown
// keys kept), get, set, unset — because a later round's setup pane writes this
// same file through this same tool, and a bigger verb set here is a bigger
// contract there.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// key is one known dotted key: a provider field (provider set) or a free
// field (provider empty).
type key struct {
	provider string
	field    string
}

func (k key) String() string {
	if k.provider == "" {
		return "free." + k.field
	}
	return "providers." + k.provider + "." + k.field
}

// keyShapes is what every bad-key refusal prints.
const keyShapes = "providers.<provider>.defaultModel | providers.<provider>.enabled | free.allowTraining | free.denyPaths"

// parseKey accepts the four key shapes of the format. Whether a provider name
// is one the launcher routes is a separate question (knownProvider), because
// that refusal lists the known names instead of the shapes.
func parseKey(s string) (key, bool) {
	parts := strings.Split(s, ".")
	switch {
	case len(parts) == 3 && parts[0] == "providers" && parts[1] != "" && contains(providerFields, parts[2]):
		return key{provider: parts[1], field: parts[2]}, true
	case len(parts) == 2 && parts[0] == "free" && contains(freeFields, parts[1]):
		return key{field: parts[1]}, true
	}
	return key{}, false
}

const usage = `usage: outsource config <command>
  path [--json]           the config file, what chose it, whether it exists
  list [--json]           every known key with its value, then unknown keys (kept)
  get <key> [--json]      one key's value, or (unset)
  set <key> <value>       enabled / allowTraining: true|false; denyPaths: a JSON
                          array of globs; defaultModel: one bare model id
  unset <key>             remove a key
keys: ` + keyShapes + `
file: $OUTSOURCE_CONFIG, else $XDG_CONFIG_HOME/outsource/config.json, else ~/.config/outsource/config.json
`

// Main dispatches `outsource config <command>`. Exit codes: 0 ok, 64 usage, 1
// an I/O failure or a file that does not parse.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	cmd, rest := args[0], args[1:]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		fmt.Fprint(stdout, usage)
		return ExitOK
	}
	// --json is a flag of the read verbs only; on set it would be a value.
	jsonOut := false
	if cmd == "path" || cmd == "list" || cmd == "get" {
		kept := rest[:0:0]
		for _, a := range rest {
			if a == "--json" {
				jsonOut = true
				continue
			}
			kept = append(kept, a)
		}
		rest = kept
	}
	want := map[string]int{"path": 0, "list": 0, "get": 1, "set": 2, "unset": 1}
	n, known := want[cmd]
	if !known {
		fmt.Fprintf(stderr, "outsource config: unknown command %q\n%s", cmd, usage)
		return ExitUsage
	}
	if len(rest) != n {
		fmt.Fprintf(stderr, "outsource config %s: takes %d argument(s), got %d\n%s", cmd, n, len(rest), usage)
		return ExitUsage
	}
	switch cmd {
	case "path":
		return cmdPath(stdout, jsonOut)
	case "list":
		return cmdList(stdout, stderr, jsonOut)
	case "get":
		return cmdGet(rest[0], stdout, stderr, jsonOut)
	case "set":
		return cmdSet(rest[0], rest[1], stdout, stderr)
	default:
		return cmdUnset(rest[0], stdout, stderr)
	}
}

// cmdPath answers the first question of anyone debugging "why is provider X
// disabled": which file, chosen by what, and is it there.
func cmdPath(stdout io.Writer, jsonOut bool) int {
	path, source := ResolvePath()
	exists := "no"
	if _, err := os.Stat(path); err == nil {
		exists = "yes"
	} else if !errors.Is(err, os.ErrNotExist) {
		exists = "unknown (" + err.Error() + ")"
	}
	if jsonOut {
		fmt.Fprintf(stdout, "%s\n", encode(struct {
			Path   string `json:"path"`
			Source string `json:"source"`
			Exists bool   `json:"exists"`
		}{path, source, exists == "yes"}))
		return ExitOK
	}
	fmt.Fprintf(stdout, "%s\nsource: %s\nexists: %s\n", path, source, exists)
	return ExitOK
}

// providerNames is the injected table, or nil after printing why there is
// none — a binary that forgot the injection must say so, not list nothing.
func providerNames(stderr io.Writer) ([]string, bool) {
	if KnownProviders == nil || QualifierFor == nil {
		fmt.Fprintln(stderr, "outsource config: this binary was built without the provider table (config.KnownProviders / config.QualifierFor are not injected)")
		return nil, false
	}
	return KnownProviders(), true
}

// resolveKey parses a key and checks its provider against the table; ok is
// false after the refusal is printed.
func resolveKey(s string, stderr io.Writer) (key, bool) {
	k, ok := parseKey(s)
	if !ok {
		fmt.Fprintf(stderr, "outsource config: unknown key %q — valid keys: %s\n", s, keyShapes)
		return key{}, false
	}
	if k.provider == "" {
		return k, true
	}
	names, ok := providerNames(stderr)
	if !ok {
		return key{}, false
	}
	if !contains(names, k.provider) {
		fmt.Fprintf(stderr, "outsource config: unknown provider %q in %s — known providers: %s\n", k.provider, s, strings.Join(names, " "))
		return key{}, false
	}
	return k, true
}

// load is Load for the CLI: a file that cannot be read or parsed is exit 1,
// and set refuses to write over it — a rewrite would drop what it could not
// read.
func load(stderr io.Writer) (*Config, bool) {
	c, err := Load()
	if err != nil {
		fmt.Fprintf(stderr, "outsource config: %v\n", err)
		return nil, false
	}
	return c, true
}

// value answers one known key: the decoded value, or nil when the file does
// not have it.
func (c *Config) value(k key) any {
	switch k.field {
	case "defaultModel":
		if m, ok := c.DefaultModel(k.provider); ok {
			return m
		}
	case "enabled":
		if v, ok := c.Enabled(k.provider); ok {
			return v
		}
	case "allowTraining":
		if v, ok := c.AllowTraining(); ok {
			return v
		}
	case "denyPaths":
		if v, ok := c.DenyPaths(); ok {
			return v
		}
	}
	return nil
}

// allKeys is every known key: each provider's fields in table order, then the
// free fields.
func allKeys(names []string) []key {
	var out []key
	for _, n := range names {
		for _, f := range providerFields {
			out = append(out, key{provider: n, field: f})
		}
	}
	for _, f := range freeFields {
		out = append(out, key{field: f})
	}
	return out
}

// text is how a value reads in the plain listing: a string bare, the rest as
// JSON, an absent key as (unset).
func text(v any) string {
	if v == nil {
		return "(unset)"
	}
	if s, ok := v.(string); ok {
		return s
	}
	return string(encode(v))
}

func cmdList(stdout, stderr io.Writer, jsonOut bool) int {
	names, ok := providerNames(stderr)
	if !ok {
		return ExitUsage
	}
	c, ok := load(stderr)
	if !ok {
		return ExitIO
	}
	keys := allKeys(names)
	unknown := c.UnknownKeys(names)
	if jsonOut {
		values := map[string]any{}
		for _, k := range keys {
			values[k.String()] = c.value(k)
		}
		fmt.Fprintf(stdout, "%s\n", encode(struct {
			Path    string         `json:"path"`
			Values  map[string]any `json:"values"`
			Unknown []string       `json:"unknown"`
		}{c.Path, values, unknown}))
		return ExitOK
	}
	fmt.Fprintf(stdout, "# %s\n", c.Path)
	for _, k := range keys {
		fmt.Fprintf(stdout, "%s = %s\n", k, text(c.value(k)))
	}
	for _, k := range unknown {
		fmt.Fprintf(stdout, "%s = unknown (kept)\n", k)
	}
	return ExitOK
}

func cmdGet(s string, stdout, stderr io.Writer, jsonOut bool) int {
	k, ok := resolveKey(s, stderr)
	if !ok {
		return ExitUsage
	}
	c, ok := load(stderr)
	if !ok {
		return ExitIO
	}
	v := c.value(k)
	if jsonOut {
		fmt.Fprintf(stdout, "%s\n", encode(v))
		return ExitOK
	}
	fmt.Fprintln(stdout, text(v))
	return ExitOK
}

// parseValue turns the command-line value for k into the raw JSON to store,
// or a refusal. The type rule is checkField's, the same one load applies to
// the file, and defaultModel adds CheckDefaultModel — the same check the
// launcher applies to a hand-edited file.
func parseValue(k key, s string) (json.RawMessage, string) {
	var raw json.RawMessage
	switch k.field {
	case "enabled", "allowTraining":
		if s != "true" && s != "false" {
			return nil, fmt.Sprintf("%s takes true or false, got: %s", k, s)
		}
		raw = json.RawMessage(s)
	case "denyPaths":
		v, msg := denyPathsValue(json.RawMessage(s))
		if msg != "" {
			return nil, fmt.Sprintf("%s takes %s, got: %s", k, msg, s)
		}
		raw = encode(v)
	case "defaultModel":
		if msg := CheckDefaultModel(k.provider, QualifierFor(k.provider), s); msg != "" {
			return nil, fmt.Sprintf("%s: %s", k, msg)
		}
		raw = encode(s)
	}
	if msg := checkField(k.field, raw); msg != "" {
		return nil, fmt.Sprintf("%s takes %s, got: %s", k, msg, s)
	}
	return raw, ""
}

func cmdSet(s, value string, stdout, stderr io.Writer) int {
	k, ok := resolveKey(s, stderr)
	if !ok {
		return ExitUsage
	}
	raw, msg := parseValue(k, value)
	if msg != "" {
		fmt.Fprintf(stderr, "outsource config: %s\n", msg)
		return ExitUsage
	}
	c, ok := load(stderr)
	if !ok {
		return ExitIO
	}
	c.set(k, raw)
	if err := c.write(); err != nil {
		fmt.Fprintf(stderr, "outsource config: %v\n", err)
		return ExitIO
	}
	fmt.Fprintf(stdout, "%s = %s (%s)\n", k, text(c.value(k)), c.Path)
	return ExitOK
}

func cmdUnset(s string, stdout, stderr io.Writer) int {
	k, ok := resolveKey(s, stderr)
	if !ok {
		return ExitUsage
	}
	c, ok := load(stderr)
	if !ok {
		return ExitIO
	}
	// Nothing to remove is not a write: unset on a missing file must not
	// create one.
	if !c.unset(k) {
		fmt.Fprintf(stdout, "%s was not set (%s)\n", k, c.Path)
		return ExitOK
	}
	if err := c.write(); err != nil {
		fmt.Fprintf(stderr, "outsource config: %v\n", err)
		return ExitIO
	}
	fmt.Fprintf(stdout, "%s unset (%s)\n", k, c.Path)
	return ExitOK
}
