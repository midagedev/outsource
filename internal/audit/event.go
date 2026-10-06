package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Event kinds. The names follow Apache Maka's RuntimeEvent content kinds
// (function_call, function_response, error) and actions (permissionDecision,
// tokenUsage → model_request) so the vocabulary is not this repo's invention;
// message_received, subagent_spawn and termination are this tool's own, for
// facts Maka's log has no slot for.
const (
	KindModelRequest       = "model_request"
	KindFunctionCall       = "function_call"
	KindFunctionResponse   = "function_response"
	KindPermissionDecision = "permission_decision"
	KindMessageReceived    = "message_received"
	KindSubagentSpawn      = "subagent_spawn"
	KindError              = "error"
	KindTermination        = "termination"
)

// Event is one thing the round did, in one shape whatever the harness.
// Every event carries seq/ts/kind/agent; the rest is kind-specific and
// omitted when empty, so one line of --json reads as what happened.
type Event struct {
	Seq   int    `json:"seq"`
	TS    string `json:"ts"`
	Kind  string `json:"kind"`
	Agent string `json:"agent"` // "" = the main loop; else the subagent id

	// model_request
	Model        string `json:"model,omitempty"`
	InputTokens  int64  `json:"input_tokens,omitempty"`
	OutputTokens int64  `json:"output_tokens,omitempty"`

	// function_call / function_response / permission_decision / error
	CallID      string `json:"call_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Input       string `json:"input,omitempty"` // full input JSON, cut to 2000 bytes
	InputSHA256 string `json:"input_sha256,omitempty"`
	IsError     *bool  `json:"is_error,omitempty"` // set on every function_response
	Bytes       int64  `json:"bytes,omitempty"`    // claude-code only: result text length
	SHA256      string `json:"sha256,omitempty"`   // claude-code only: result text hash
	Head        string `json:"head,omitempty"`     // resp: first 300 bytes; message: first 200
	State       string `json:"state,omitempty"`    // agy only: DONE or ERROR
	Decision    string `json:"decision,omitempty"` // permission_decision: always "deny" today
	By          string `json:"by,omitempty"`       // git-guard | mod | hook | permission
	Text        string `json:"text,omitempty"`     // permission_decision: the refusal text (cut)

	// message_received
	From       string `json:"from,omitempty"`
	FromName   string `json:"from_name,omitempty"`
	TextSHA256 string `json:"text_sha256,omitempty"`

	// subagent_spawn
	SubagentType string `json:"subagent_type,omitempty"`

	// termination (from the .rc sentinel)
	RC             int    `json:"rc,omitempty"`
	DoneMarker     string `json:"done_marker,omitempty"`
	ToolCalls      string `json:"tool_calls,omitempty"`
	ModelRequested string `json:"model_requested,omitempty"`
	ModelActual    string `json:"model_actual,omitempty"`
	Seal           string `json:"seal,omitempty"`
}

// Trail is one parsed round: the event list plus the side tables the summary
// needs and that a cut-to-2000-bytes event line must not be the only home of
// (full tool inputs, agy's init.tools, the subagent inventory).
type Trail struct {
	Events []Event
	// Inputs maps call_id → the tool's decoded input, uncut.
	Inputs map[string]map[string]any
	// Spawns maps the Agent call_id → its input, the measured link (through
	// the subagent sidecar's toolUseId) to the model a spawn asked for.
	Spawns map[string]map[string]any
	// InitTools is agy's init.tools list (empty on claude-code); the write-tool
	// and command-tool names are confirmed against it, not assumed.
	InitTools []string
	// InitModel is agy's init.model — the model id every agy request belongs to.
	InitModel string
	// Subagents is one row per subagents/*.jsonl, in file order.
	Subagents []Subagent
	// Sentinel is the parsed <log>.rc, nil while the round is running.
	Sentinel map[string]string
}

// Subagent is what the summary says about one subagent transcript: where it
// came from (linked through its .meta.json toolUseId when that file exists)
// and which models answered in it.
type Subagent struct {
	ID         string
	Type       string // spawn input subagent_type, or meta agentType
	SpawnModel string // the model the Agent call asked for, "" when unset
	Models     []string
}

// sha256hex is the one hash spelling this package uses.
func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// cut keeps the first n bytes of s, whole runes preferred, and marks a cut with
// an ellipsis so a reader never mistakes a head for the whole text.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s[:n])
	for len(b) > 0 && !utf8OK(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return string(b) + "…"
}

func utf8OK(b byte) bool { return b&0xC0 != 0x80 }

// ParseTrail reads one round's evidence file per harness and returns the
// uniform event list. This is a third reader of the claude-code transcript
// (tail and launch/toolcalls.go are the other two); that duplication is a
// known, deliberate cost — the structured event model does not fit either
// reader's job, and forcing it in would change their output contracts.
func ParseTrail(path, harness string) (*Trail, error) {
	switch harness {
	case "claude-code":
		return parseClaudeTrail(path)
	case "agy":
		return parseAgyTrail(path)
	default:
		return nil, fmt.Errorf("harness %s is not supported yet", harness)
	}
}

// Finalize assigns seq numbers, orders the list, and attaches the termination
// event built from the sentinel (none while the round is running). It is the
// single place the event list is arranged, so every output mode sees the same
// order.
func (t *Trail) Finalize(seal Seal) {
	if t.Sentinel != nil {
		t.Events = append(t.Events, terminationEvent(t.Sentinel, seal))
	}
	// Chronological interleaving when every event carries a timestamp — the
	// subagent transcripts overlap the main loop and a review reads better in
	// real order. RFC3339 stamps in one zone sort lexicographically, and every
	// claude-code line stamp is UTC. agy events carry no ts at all, so the
	// guard keeps their file order rather than shuffling them to the front.
	allStamped := len(t.Events) > 0
	for _, e := range t.Events {
		if e.TS == "" {
			allStamped = false
			break
		}
	}
	if allStamped {
		sort.SliceStable(t.Events, func(i, j int) bool { return t.Events[i].TS < t.Events[j].TS })
	}
	for i := range t.Events {
		t.Events[i].Seq = i + 1
	}
}

// terminationEvent is the last event: what the sentinel says about how the
// round ended, sealed or not.
func terminationEvent(m map[string]string, seal Seal) Event {
	rc := 0
	fmt.Sscanf(m["rc"], "%d", &rc)
	return Event{
		TS:             m["finished"],
		Kind:           KindTermination,
		RC:             rc,
		DoneMarker:     m["done_marker"],
		ToolCalls:      m["tool_calls"],
		ModelRequested: m["model_requested"],
		ModelActual:    m["model_actual"],
		Seal:           seal.Verdict,
	}
}

// ---- the claude-code transcript --------------------------------------------
//
// One JSON object per line. The shapes below were measured on 2026-10-06
// against real round transcripts (CLI 2.1.290): an assistant response is split
// across several lines that share one message.id; tool results arrive in user
// lines as tool_result parts; a cross-session message appears up to three
// times (queue-operation enqueue, queued_command attachment, queue-operation
// remove); a denial by hook or mod is an is_error tool_result whose line also
// carries toolDenialKind.

type ccLine struct {
	Type           string `json:"type"`
	Timestamp      string `json:"timestamp"`
	ToolDenialKind string `json:"toolDenialKind"`
	Operation      string `json:"operation"`
	Content        string `json:"content"` // queue-operation payload
	Message        ccMessage
	Attachment     ccAttachment `json:"attachment"`
}

// SyntheticModel is the model label Claude Code puts on the assistant lines it
// writes itself (API errors, interruptions); no model produced them.
const SyntheticModel = "<synthetic>"

// firstText is the first text part of a message's content, for an error head.
func firstText(raw json.RawMessage) string {
	var parts []ccPart
	if json.Unmarshal(raw, &parts) != nil {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	for _, p := range parts {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}

type ccMessage struct {
	Role    string          `json:"role"`
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

type ccAttachment struct {
	Type      string `json:"type"`
	Prompt    string `json:"prompt"`
	Timestamp string `json:"timestamp"`
	Origin    *struct {
		From string `json:"from"`
		Name string `json:"name"`
	} `json:"origin"`
}

type ccPart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// parseClaudeTrail reads the main transcript and every subagents/*.jsonl.
func parseClaudeTrail(path string) (*Trail, error) {
	t := &Trail{Inputs: map[string]map[string]any{}, Spawns: map[string]map[string]any{}}
	evs, err := parseClaudeFile(path, "", t)
	if err != nil {
		return nil, err
	}
	t.Events = append(t.Events, evs...)

	// The subagents directory sits beside the transcript, named after it:
	// <session>.jsonl → <session>/subagents/*.jsonl. The id in each file name
	// is what tags that file's events.
	dir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	ents, err := os.ReadDir(dir)
	if err == nil {
		var files []string
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".jsonl") && !e.IsDir() {
				files = append(files, e.Name())
			}
		}
		sort.Strings(files)
		for _, name := range files {
			id := strings.TrimSuffix(name, ".jsonl")
			sub := Subagent{ID: id}
			// The sidecar is <id>.meta.json beside <id>.jsonl (measured
			// 2026-10-06, cfg-spawn-pin) — not <id>.jsonl.meta.json.
			if meta := readSubagentMeta(filepath.Join(dir, id+".meta.json")); meta != nil {
				sub.Type = meta.AgentType
				sub.SpawnModel = spawnModelFor(t.Spawns, meta.ToolUseID)
			}
			evs, err := parseClaudeFile(filepath.Join(dir, name), id, t)
			if err != nil {
				return nil, err
			}
			sub.Models = modelsOf(evs)
			t.Events = append(t.Events, evs...)
			t.Subagents = append(t.Subagents, sub)
		}
	}
	return t, nil
}

// ccSubagentMeta is the sidecar each subagent transcript carries. It is the
// only measured link between a subagents/<id>.jsonl file and the Agent call
// that spawned it (its toolUseId), which is how a spawn's requested model
// reaches the subagent's drift verdict.
type ccSubagentMeta struct {
	AgentType string `json:"agentType"`
	ToolUseID string `json:"toolUseId"`
	Model     string `json:"model"`
}

func readSubagentMeta(path string) *ccSubagentMeta {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m ccSubagentMeta
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}

// spawnModelFor returns the model a subagent's spawn asked for, through the
// sidecar's toolUseId link into the main loop's Agent calls.
func spawnModelFor(spawns map[string]map[string]any, callID string) string {
	if callID == "" {
		return ""
	}
	s, _ := spawns[callID]["model"].(string)
	return s
}

// parseClaudeFile walks one transcript file into events. agentTag is "" for
// the main loop or the subagent id taken from the file name.
func parseClaudeFile(path, agentTag string, t *Trail) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Transcript lines run large — a measured working round's longest was
	// 215,623 bytes (attachment blocks) — so the cap matches toolcalls.go's.
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)

	var out []Event
	lastMsgID := "\x00none" // distinct-id tracking; ids are msg_* and never this
	seenMessages := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var L ccLine
		if json.Unmarshal([]byte(line), &L) != nil {
			continue // a line this tool does not understand is not news
		}
		switch L.Type {
		case "queue-operation":
			if L.Operation == "enqueue" && strings.HasPrefix(L.Content, "<cross-session-message") {
				if e, ok := messageEvent(L.Content, "", "", L.Timestamp, agentTag, seenMessages); ok {
					out = append(out, e)
				}
			}
		case "attachment":
			if L.Attachment.Type == "queued_command" && strings.HasPrefix(L.Attachment.Prompt, "<cross-session-message") {
				var from, name string
				if L.Attachment.Origin != nil {
					from, name = L.Attachment.Origin.From, L.Attachment.Origin.Name
				}
				ts := L.Timestamp
				if L.Attachment.Timestamp != "" {
					ts = L.Attachment.Timestamp
				}
				if e, ok := messageEvent(L.Attachment.Prompt, from, name, ts, agentTag, seenMessages); ok {
					out = append(out, e)
				}
			}
		case "assistant":
			// One response is split across lines that share message.id; the
			// request (and its usage) belongs to the id, not the line.
			id := L.Message.ID
			if id == "" {
				id = fmt.Sprintf("line-%d", len(out)) // no id: one request per line
			}
			if L.Message.Model == SyntheticModel {
				// Claude Code writes its own messages (an API error such as a
				// 429, an interruption) as assistant lines labelled <synthetic>.
				// No model answered them, so they are errors, not requests —
				// counted as requests they read as MODEL DRIFT (measured
				// 2026-10-06: a round cut by z.ai's 5-hour limit).
				out = append(out, Event{
					TS:    L.Timestamp,
					Kind:  KindError,
					Agent: agentTag,
					Head:  cut(firstText(L.Message.Content), 300),
				})
				lastMsgID = id
				continue
			}
			if id != lastMsgID {
				out = append(out, Event{
					TS:           L.Timestamp,
					Kind:         KindModelRequest,
					Agent:        agentTag,
					Model:        L.Message.Model,
					InputTokens:  L.Message.Usage.InputTokens,
					OutputTokens: L.Message.Usage.OutputTokens,
				})
				lastMsgID = id
			}
			var parts []ccPart
			if json.Unmarshal(L.Message.Content, &parts) != nil {
				continue // content as a plain string: the spec text, not a call
			}
			for _, p := range parts {
				if p.Type != "tool_use" {
					continue
				}
				out = append(out, t.callEvent(p, L.Timestamp, agentTag))
				// An Agent call is also a spawn: the type and model it asked
				// for are what the Subagents section compares against what the
				// subagent actually ran.
				if agentTag == "" && p.Name == "Agent" {
					if e, ok := subagentSpawnEvent(p.ID, t.Inputs[p.ID], L.Timestamp); ok {
						out = append(out, e)
					}
				}
			}
		case "user":
			var parts []ccPart
			if json.Unmarshal(L.Message.Content, &parts) != nil {
				continue
			}
			for _, p := range parts {
				if p.Type != "tool_result" {
					continue
				}
				text := flattenResult(p.Content)
				isErr := p.IsError
				out = append(out, Event{
					TS:      L.Timestamp,
					Kind:    KindFunctionResponse,
					Agent:   agentTag,
					CallID:  p.ToolUseID,
					IsError: &isErr,
					Bytes:   int64(len(text)),
					SHA256:  sha256hex([]byte(text)),
					Head:    cut(text, 300),
				})
				if !p.IsError {
					continue
				}
				if by, text := recogniseDenial(text); by != "" {
					out = append(out, Event{
						TS:       L.Timestamp,
						Kind:     KindPermissionDecision,
						Agent:    agentTag,
						CallID:   p.ToolUseID,
						Decision: "deny",
						By:       by,
						Text:     cut(text, 300),
					})
				} else if L.ToolDenialKind != "" {
					// The line itself is marked as a denial (measured value:
					// permission-rule) while the text matched no known
					// refusal — the harness's own permission layer said no.
					out = append(out, Event{
						TS:       L.Timestamp,
						Kind:     KindPermissionDecision,
						Agent:    agentTag,
						CallID:   p.ToolUseID,
						Decision: "deny",
						By:       "permission",
						Text:     cut(text, 300),
					})
				} else {
					out = append(out, Event{
						TS:     L.Timestamp,
						Kind:   KindError,
						Agent:  agentTag,
						CallID: p.ToolUseID,
						Head:   cut(text, 300),
					})
				}
			}
		}
	}
	return out, sc.Err()
}

// callEvent turns one tool_use block into a function_call event.
func (t *Trail) callEvent(p ccPart, ts, agentTag string) Event {
	e := Event{
		TS:          ts,
		Kind:        KindFunctionCall,
		Agent:       agentTag,
		CallID:      p.ID,
		Name:        p.Name,
		Input:       cut(string(p.Input), 2000),
		InputSHA256: sha256hex(p.Input),
	}
	if len(p.Input) > 0 {
		var in map[string]any
		if json.Unmarshal(p.Input, &in) == nil && in != nil {
			t.Inputs[p.ID] = in
			if agentTag == "" && p.Name == "Agent" {
				t.Spawns[p.ID] = in
			}
		}
	}
	return e
}

// subagentSpawnEvent is the Agent call's own summary: what it asked to spawn
// and on which model. Emitted right after the call's function_call.
func subagentSpawnEvent(callID string, in map[string]any, ts string) (Event, bool) {
	if in == nil {
		return Event{}, false
	}
	st, _ := in["subagent_type"].(string)
	if st == "" {
		return Event{}, false
	}
	model, _ := in["model"].(string)
	return Event{
		TS:           ts,
		Kind:         KindSubagentSpawn,
		CallID:       callID,
		SubagentType: st,
		Model:        model,
	}, true
}

// messageEvent builds the one event a received cross-session message becomes.
// The same message arrives up to three times (enqueue, queued_command
// attachment, remove); the full text is the identity, so the second and third
// sightings are dropped rather than double-counted.
func messageEvent(text, from, fromName, ts, agentTag string, seen map[string]bool) (Event, bool) {
	if seen[text] {
		return Event{}, false
	}
	seen[text] = true
	body := text
	if end := strings.Index(body, "</cross-session-message>"); end >= 0 {
		body = body[:end]
	}
	if _, rest, ok := strings.Cut(body, ">"); ok {
		body = rest
	}
	if from == "" || fromName == "" {
		for _, k := range []string{"from", "from-name"} {
			if v, ok := attr(text, k); ok {
				if k == "from" {
					from = v
				} else {
					fromName = v
				}
			}
		}
	}
	return Event{
		TS:         ts,
		Kind:       KindMessageReceived,
		Agent:      agentTag,
		From:       from,
		FromName:   fromName,
		TextSHA256: sha256hex([]byte(body)),
		Head:       cut(body, 200),
	}, true
}

// attr pulls one attribute out of the opening <cross-session-message …> tag.
func attr(text, key string) (string, bool) {
	for _, seg := range strings.Fields(text) {
		if k, v, ok := strings.Cut(seg, "="); ok && k == key && len(v) >= 2 && v[0] == '"' {
			return strings.TrimSuffix(v[1:], "\""), true
		}
	}
	return "", false
}

// flattenResult turns a tool result's content — a string, or a part array —
// into its text. The same shape-merging tail does; kept local because tail's
// is unexported and this package must not grow a dependency on its renderer.
func flattenResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []ccPart
	if json.Unmarshal(raw, &parts) == nil {
		var b []string
		for _, p := range parts {
			if p.Text != "" {
				b = append(b, p.Text)
			}
		}
		if len(b) > 0 {
			return strings.Join(b, " ")
		}
	}
	return string(raw)
}

func modelsOf(evs []Event) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == KindModelRequest && e.Model != "" {
			out = append(out, e.Model)
		}
	}
	return out
}

// ---- the agy stream-json log ------------------------------------------------
//
// One event per line: init (model + tools), step_update (step_index, state,
// step_type; tool steps carry tool_name and tool_info.parameters, DONE steps
// carry output, ERROR steps carry error.message; agent_response DONE steps
// carry usage), and a final result. Measured 2026-10-06 on real round logs.

type agyLine struct {
	Event      string `json:"event"`
	StepUpdate struct {
		StepIndex int    `json:"step_index"`
		State     string `json:"state"`
		StepType  string `json:"step_type"`
		ToolName  string `json:"tool_name"`
		ToolInfo  struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
			Error      *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"tool_info"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"step_update"`
	Init struct {
		Model string   `json:"model"`
		Tools []string `json:"tools"`
	} `json:"init"`
}

func parseAgyTrail(path string) (*Trail, error) {
	t := &Trail{Inputs: map[string]map[string]any{}}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var L agyLine
		if json.Unmarshal([]byte(line), &L) != nil {
			continue
		}
		switch L.Event {
		case "init":
			t.InitModel = L.Init.Model
			t.InitTools = L.Init.Tools
		case "step_update":
			su := L.StepUpdate
			id := fmt.Sprintf("step-%d", su.StepIndex)
			switch {
			case su.StepType == "tool" && su.State == "ACTIVE":
				e := Event{
					Kind:        KindFunctionCall,
					CallID:      id,
					Name:        su.ToolName,
					Input:       cut(string(su.ToolInfo.Parameters), 2000),
					InputSHA256: sha256hex(su.ToolInfo.Parameters),
				}
				if len(su.ToolInfo.Parameters) > 0 {
					var in map[string]any
					if json.Unmarshal(su.ToolInfo.Parameters, &in) == nil && in != nil {
						t.Inputs[id] = in
					}
				}
				t.Events = append(t.Events, e)
			case su.StepType == "tool" && su.State == "DONE":
				no := false
				t.Events = append(t.Events, Event{
					Kind:    KindFunctionResponse,
					CallID:  id,
					State:   "DONE",
					IsError: &no,
				})
			case su.StepType == "tool" && su.State == "ERROR":
				yes := true
				t.Events = append(t.Events, Event{
					Kind:    KindFunctionResponse,
					CallID:  id,
					State:   "ERROR",
					IsError: &yes,
				})
				msg := ""
				if su.ToolInfo.Error != nil {
					msg = su.ToolInfo.Error.Message
				}
				if by, txt := recogniseDenial(msg); by != "" {
					t.Events = append(t.Events, Event{
						Kind:     KindPermissionDecision,
						CallID:   id,
						Decision: "deny",
						By:       by,
						Text:     cut(txt, 300),
					})
				} else {
					t.Events = append(t.Events, Event{
						Kind:   KindError,
						CallID: id,
						Head:   cut(msg, 300),
					})
				}
			case su.StepType == "agent_response" && su.State == "DONE":
				// One per DONE step, whatever the usage says: a step without
				// usage is still a model request, counted with zeros.
				t.Events = append(t.Events, Event{
					Kind:         KindModelRequest,
					Model:        t.InitModel,
					InputTokens:  su.Usage.InputTokens,
					OutputTokens: su.Usage.OutputTokens,
				})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return t, nil
}
