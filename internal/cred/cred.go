// Package cred is the single owner of credential resolution — and of which host
// a provider's account lives on.
//
// One owner, because the alternative was measured: the key lookup used to be
// duplicated across the launcher, the quota reader and the crushrc, and they
// drifted. Adding a provider means adding it here, not in three places.
//
// A resolved key is printed on stdout and nowhere else. It is never written into
// a file this code creates, never passed on a command line, and never logged.
// Callers capture stdout.
package cred

import (
	"encoding/json"
	"fmt"
	"github.com/midagedev/outsource/internal/telemetry"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	ExitNotFound = 1
	ExitUsage    = 64
)

// Provider is one backend's credential identity.
type Provider struct {
	Name      string
	EnvVar    string // the environment variable that beats everything
	BaseEnv   string // the base-URL override variable
	ZaiFamily bool   // participates in the z.ai discovery chain and host choice
	// OpencodeAuthID, when set, is the entry in opencode's auth store
	// (OpencodeAuthPath) that holds this provider's key: a read-only
	// discovery source after the 0600 store, the same kind of discovery the
	// z.ai chain does for other tools' files. That file is opencode's; this
	// package never writes it and never copies the key out of it.
	OpencodeAuthID string
}

var providers = map[string]Provider{
	"zai": {Name: "zai", EnvVar: "ZAI_API_KEY", BaseEnv: "ZAI_BASE_URL", ZaiFamily: true},
	"xai": {Name: "xai", EnvVar: "XAI_API_KEY", BaseEnv: "XAI_BASE_URL"},
	// OpenRouter runs on the claude-code harness since 2026-10-09 (its
	// Anthropic-compatible endpoint answered tool calls; see the openrouter
	// row in internal/launch/wiring.go), and that harness resolves its key
	// here. A user who ran `opencode auth login` already has one, in
	// opencode's auth store, under "openrouter".
	//
	// The base override is OUTSOURCE_OPENROUTER_BASE_URL, not OPENROUTER_BASE_URL:
	// other tools set that one to the OpenAI-compatible root (…/api/v1), and the
	// claude CLI appends /v1/messages to its base, so honouring it would send
	// rounds to …/api/v1/v1/messages.
	"openrouter": {Name: "openrouter", EnvVar: "OPENROUTER_API_KEY", BaseEnv: "OUTSOURCE_OPENROUTER_BASE_URL", OpencodeAuthID: "openrouter"},
}

// knownProviders is the provider names, sorted, for every message that lists
// them. Derived from the map, so a new row cannot be resolvable and missing
// from the refusal that names what is known (the hand-written list of zai and
// xai went stale exactly that way when openrouter was added).
func knownProviders() []string {
	out := make([]string, 0, len(providers))
	for name := range providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// OpencodeAuthPath is the single owner of where opencode keeps its auth
// store: $XDG_DATA_HOME/opencode/auth.json, by default
// ~/.local/share/opencode/auth.json. "" when no home directory can be found —
// every reader treats that as "cannot tell" and fails open. The launcher's
// opencode credential pre-flight reads the same file through this function.
func OpencodeAuthPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode", "auth.json")
}

// zaiHosts are the hosts a z.ai coding plan is served from. The coding plan is
// api.z.ai globally and open.bigmodel.cn in mainland China, and the same key 401s
// against the wrong one — which is why the host is resolved here rather than
// assumed at a call site.
var zaiHosts = []string{"api.z.ai", "open.bigmodel.cn", "dev.bigmodel.cn"}

type paths struct {
	store          string
	crushConfig    string
	claudeSettings string
	chelperConfig  string
	opencodeAuth   string
}

func resolvePaths() paths {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	claudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeDir == "" {
		claudeDir = filepath.Join(home, ".claude")
	}
	return paths{
		store:          filepath.Join(configHome, "outsource", "credentials"),
		crushConfig:    filepath.Join(configHome, "crush", "crush.json"),
		claudeSettings: filepath.Join(claudeDir, "settings.json"),
		chelperConfig:  filepath.Join(home, ".chelper", "config.yaml"),
		opencodeAuth:   OpencodeAuthPath(),
	}
}

// chelperField reads one scalar out of the vendor installer's YAML. Deliberately
// not a YAML parser: the file is written by `npx @z_ai/coding-helper` with one
// flat `key: value` per line, and the shell read it with sed. Quotes and a
// trailing CR are stripped, as they were there.
func chelperField(path, field string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := field + ":"
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimLeft(line[len(prefix):], " \t")
		return strings.Trim(v, "\"'\r")
	}
	return ""
}

// settingsEnv reads the `env` map out of a Claude Code settings file.
func settingsEnv(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var d struct {
		Env map[string]any `json:"env"`
	}
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range d.Env {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// BaseURL answers which host this account lives on, given the provider's default.
// An explicit environment override wins outright; otherwise the vendor
// installer's `plan:` field decides, then Claude Code's configured base URL, and
// otherwise the default is returned unchanged.
func BaseURL(p Provider, def string, pp paths) string {
	if v := os.Getenv(p.BaseEnv); v != "" {
		return v
	}
	host := ""
	if p.ZaiFamily {
		switch plan := chelperField(pp.chelperConfig, "plan"); {
		case plan == "":
		case plan == "glm_coding_plan_global":
			host = "https://api.z.ai"
		default:
			host = "https://open.bigmodel.cn"
		}
		if host == "" {
			if env := settingsEnv(pp.claudeSettings); env != nil {
				if u, err := url.Parse(env["ANTHROPIC_BASE_URL"]); err == nil {
					for _, h := range zaiHosts {
						if u.Hostname() == h {
							host = u.Scheme + "://" + u.Host
							break
						}
					}
				}
			}
		}
	}
	if host == "" {
		return def
	}
	// Keep the default's path, swap its origin — the monitor and Anthropic-
	// compatible paths are the same on both hosts.
	return host + stripOrigin(def)
}

// stripOrigin removes a leading scheme://host, matching the shell's
// sed -E 's#^[a-z]+://[^/]*##'.
func stripOrigin(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	rest := u[i+3:]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[j:]
	}
	return ""
}

// source is one place a key might live, in the order they are consulted.
type source struct {
	note string             // what the failure message says was tried
	read func(paths) string // "" when this source has nothing
}

func sources(p Provider) []source {
	out := []source{
		{note: "$" + p.EnvVar + " (environment)", read: func(paths) string { return os.Getenv(p.EnvVar) }},
	}
	pp := resolvePaths()
	out = append(out, source{
		note: pp.store + " (written by bin/setup-key.sh)",
		read: func(pp paths) string { return storeValue(pp.store, p.EnvVar) },
	})
	if p.OpencodeAuthID != "" && pp.opencodeAuth != "" {
		out = append(out, source{
			note: pp.opencodeAuth + " (" + p.OpencodeAuthID + ".key, written by `opencode auth login`; read only)",
			read: func(pp paths) string { return opencodeAuthKey(pp.opencodeAuth, p.OpencodeAuthID) },
		})
	}
	if !p.ZaiFamily {
		return out
	}
	return append(out,
		source{
			note: pp.chelperConfig + " (api_key, written by npx @z_ai/coding-helper)",
			read: func(pp paths) string { return chelperField(pp.chelperConfig, "api_key") },
		},
		source{
			note: pp.crushConfig + " (providers.zai.api_key)",
			read: func(pp paths) string { return crushKey(pp.crushConfig) },
		},
		source{
			// Only when its base URL is a z.ai host, so a real Anthropic token can
			// never be lifted out of a user's own settings.
			note: pp.claudeSettings + " (env.ANTHROPIC_AUTH_TOKEN, only when env.ANTHROPIC_BASE_URL is a z.ai host)",
			read: func(pp paths) string { return claudeZaiToken(pp.claudeSettings) },
		},
	)
}

func storeValue(path, envVar string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := envVar + "="
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, prefix) {
			return line[len(prefix):]
		}
	}
	return ""
}

func crushKey(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var d struct {
		Providers map[string]struct {
			APIKey string `json:"api_key"`
		} `json:"providers"`
	}
	if json.Unmarshal(b, &d) != nil {
		return ""
	}
	return d.Providers["zai"].APIKey
}

// opencodeAuthKey reads one provider's key out of opencode's auth store. Only
// that provider's entry is decoded, so an entry of another shape (an oauth
// login) cannot make the whole file unreadable. A missing or unparseable
// file, a missing entry and a whitespace-only key all read as "": no key
// here, so resolution goes on and reports what it tried. (Whitespace-only is
// also what the launcher's positively-absent check counts as no key.)
func opencodeAuthKey(path, id string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var d map[string]json.RawMessage
	if json.Unmarshal(b, &d) != nil {
		return ""
	}
	raw, ok := d[id]
	if !ok {
		return ""
	}
	var e struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	return strings.TrimSpace(e.Key)
}

func claudeZaiToken(path string) string {
	env := settingsEnv(path)
	if env == nil {
		return ""
	}
	base := env["ANTHROPIC_BASE_URL"]
	for _, h := range zaiHosts {
		if strings.Contains(base, h) {
			return env["ANTHROPIC_AUTH_TOKEN"]
		}
	}
	return ""
}

// Main is the credential entry point.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(stderr, "usage: credential <provider> [--where]")
		return ExitUsage
	}
	name := args[0]
	p, ok := providers[name]
	if !ok {
		fmt.Fprintf(stderr, "credential: unknown provider '%s' (known: %s)\n", name, strings.Join(knownProviders(), " "))
		return ExitUsage
	}
	mode := ""
	if len(args) > 1 {
		mode = args[1]
	}
	pp := resolvePaths()

	if mode == "--base-url" {
		if len(args) < 3 || args[2] == "" {
			fmt.Fprintln(stderr, "credential: --base-url needs the provider's default URL")
			return ExitUsage
		}
		fmt.Fprintln(stdout, BaseURL(p, args[2], pp))
		return 0
	}

	var found, foundIn string
	var tried []string
	for _, s := range sources(p) {
		// Every applicable source is named whether or not it answered, because
		// the failure message's whole job is to say where to look.
		tried = append(tried, s.note)
		if found != "" {
			continue
		}
		if v := s.read(pp); v != "" {
			found, foundIn = v, sourceLabel(s.note)
		}
	}
	if found == "" {
		// The blank lines between sections are load-bearing: this message is the
		// only thing a user sees when nothing is configured, and it lists five
		// paths, so the two instructions have to be findable in it rather than
		// buried at the bottom of a wall. Reproduced from the shell's heredoc
		// exactly — a first draft dropped them because the source was read
		// through a filter that stripped blank lines, and the parity gate caught
		// it.
		fmt.Fprintf(stderr, "outsource: no API key for provider '%s'. Tried, in order:\n", name)
		for _, t := range tried {
			fmt.Fprintf(stderr, "  - %s\n", t)
		}
		fmt.Fprintf(stderr, "\nSet it once, interactively:\n\n  %s %s\n\nor export it yourself:\n\n  export %s=...\n",
			setupKeyPath(), name, p.EnvVar)
		return ExitNotFound
	}
	if mode == "--where" {
		fmt.Fprintln(stdout, foundIn)
		return 0
	}
	fmt.Fprintln(stdout, found)
	return 0
}

// sourceLabel is what --where prints: the note without its parenthetical.
func sourceLabel(note string) string {
	if i := strings.Index(note, " ("); i > 0 {
		return note[:i]
	}
	return note
}

// setupKeyPath finds the one tool that is still a hand-written script. See
// siblingScript in internal/quota for the OUTSOURCE_SKILL_DIR case: a binary
// the dispatcher ran from its download cache has no files beside it, and the
// skill's bin/ — exported by the dispatcher — is where setup-key.sh lives.
func setupKeyPath() string {
	if dir := os.Getenv("OUTSOURCE_SKILL_DIR"); dir != "" {
		p := filepath.Join(dir, "bin", "setup-key.sh")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return "setup-key.sh"
	}
	return filepath.Join(filepath.Dir(exe), "setup-key.sh")
}

// ---- in-process API for the launchers --------------------------------------
// These exist so a launcher in the same binary does not spawn a subprocess to
// ask a question this package can already answer. Credential resolution keeps
// its single owner either way; only the transport changes.

// KeyOrExplain resolves a provider's key, or prints the same "tried, in order"
// explanation the CLI prints and reports false. The message stays owned here,
// because a caller writing its own would drift from the resolution order.
func KeyOrExplain(provider string, stderr io.Writer) (string, bool) {
	p, ok := providers[provider]
	if !ok {
		fmt.Fprintf(stderr, "credential: unknown provider '%s' (known: %s)\n", provider, strings.Join(knownProviders(), " "))
		return "", false
	}
	pp := resolvePaths()
	var tried []string
	for _, s := range sources(p) {
		tried = append(tried, s.note)
		if v := s.read(pp); v != "" {
			return v, true
		}
	}
	telemetry.Note("why", "no credential found for "+provider)
	fmt.Fprintf(stderr, "outsource: no API key for provider '%s'. Tried, in order:\n", provider)
	for _, t := range tried {
		fmt.Fprintf(stderr, "  - %s\n", t)
	}
	fmt.Fprintf(stderr, "\nSet it once, interactively:\n\n  %s %s\n\nor export it yourself:\n\n  export %s=...\n",
		setupKeyPath(), provider, p.EnvVar)
	return "", false
}

// Base answers which host this account lives on, given the provider's default.
func Base(provider, def string) string {
	p, ok := providers[provider]
	if !ok {
		return def
	}
	return BaseURL(p, def, resolvePaths())
}
