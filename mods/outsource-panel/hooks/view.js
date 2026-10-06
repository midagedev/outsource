// Pure presentation rules for the outsource-panel mod. Nothing in this file
// touches `$` or any host interface: every function takes plain data and
// returns plain data, so the whole display contract is testable without a
// session (and the numbers below stay the single source of truth).
//
// The numbers are the contract given by the lead's spec:
//   label 16 columns, provider·harness 10, elapsed right-aligned 6,
//   at most 8 rows drawn with `+<k> more`, finished rounds visible for
//   10800 s, ≤4 activity lines, trail 5–40 rows (10 inline).

// ---- who this copy of the panel is -------------------------------------------

// The panel ships twice from one source: inside the root plugin `outsource`
// (the marketplace install, `--plugin-dir <clone>`) and as the standalone
// plugin `outsource-panel` (`--plugin-dir <clone>/mods/outsource-panel`).
// `$.plugin.name` says which copy is running; everything model-facing that
// carries a plugin prefix is derived from it here.
export const BUNDLED_NAME = 'outsource'
export const PANEL_NAME = 'outsource-panel'

// A plugin's tools reach the model as mcp__<plugin>__<tool>.
export function toolName(pluginName, tool) {
  if (typeof pluginName !== 'string' || pluginName === '') {
    throw new Error('toolName: pluginName — $.plugin.name — is required')
  }
  return 'mcp__' + pluginName + '__' + tool
}

// What one copy does when the other one is about to join the session, as the
// engine admits modules one at a time and every module already admitted
// judges the next (`plugin.register`). Exactly one of any two is admitted
// first, and each copy carries both halves of the rule, so whichever order
// the engine picks, the bundled copy is the one that stays:
//   'refuse'     — I am the bundled copy and the standalone is joining after
//                  me: refuse it, so it never loads (no hook, tool, command);
//   'stand-down' — I am the standalone and the bundled copy is joining after
//                  me: let it in and register nothing myself;
//   null         — anything else.
export function pairVerdict(selfName, joiningName) {
  if (selfName === BUNDLED_NAME && joiningName === PANEL_NAME) return 'refuse'
  if (selfName === PANEL_NAME && joiningName === BUNDLED_NAME) return 'stand-down'
  return null
}

// Where the outsource binary may be, most specific first; the first that
// exists wins (register.js). `root` is `$.plugin.root`, the folder holding
// this copy's plugin.json:
//   envBin                  OUTSOURCE_PANEL_BIN, an explicit override;
//   <root>/skills/…         the root plugin (marketplace cache, --plugin-dir <clone>);
//   <root>/../../skills/…   the standalone mods/outsource-panel inside a clone,
//                           spelled without the `..` (the engine resolves them
//                           in $.fs.exists, so the path checked and the path
//                           run — and the one the wake spells — stay one);
//   <home>/.claude/skills/… where install.sh puts the skill.
const BIN_TAIL = '/skills/outsource/bin/outsource'
export function binCandidates({ root, home, envBin }) {
  const out = []
  if (typeof envBin === 'string' && envBin !== '') out.push(envBin)
  if (typeof root === 'string' && root !== '') {
    out.push(root + BIN_TAIL)
    out.push(parentDir(parentDir(root)) + BIN_TAIL)
  }
  if (typeof home === 'string' && home !== '') out.push(home + '/.claude' + BIN_TAIL)
  return out
}

function parentDir(path) {
  const trimmed = path.replace(/\/+$/, '')
  return trimmed.slice(0, trimmed.lastIndexOf('/'))
}

// A `$.ui.log` line as the panel writes it: one `outsource-panel: ` prefix,
// whichever copy runs, so the debug log greps the same for both. Every line
// goes to the debug log alone, never the transcript (where the host leads a
// row with `<plugin>: ` and a prefix of ours doubled it, measured
// 2026-10-06). The debug log leads it with `[<plugin>] $.ui.log (to debug): `
// (measured 2026-10-06, both copies):
//   [outsource-panel] $.ui.log (to debug): outsource-panel: <text>
//   [outsource] $.ui.log (to debug): outsource-panel: <text>
export function logText(text) {
  return PANEL_NAME + ': ' + text
}

// ---- display width -----------------------------------------------------------

// Width 2: East Asian Wide and Fullwidth code points (Unicode 16.0.0
// EastAsianWidth W|F, generated from Python's unicodedata 16.0.0 on this
// machine; every Emoji_Presentation=Yes code point in Unicode 16 is inside
// these ranges — spot-checked against the emoji set, and the panel's own
// glyphs are asserted in the tests). Width 0: the combining marks and the
// joiner/variation selector the trail glyphs carry. Everything else: 1.
const WIDE_RANGES = [
  0x1100, 0x115f, 0x231a, 0x231b, 0x2329, 0x232a, 0x23e9, 0x23ec, 0x23f0, 0x23f0, 0x23f3, 0x23f3,
  0x25fd, 0x25fe, 0x2614, 0x2615, 0x2630, 0x2637, 0x2648, 0x2653, 0x267f, 0x267f, 0x268a, 0x268f,
  0x2693, 0x2693, 0x26a1, 0x26a1, 0x26aa, 0x26ab, 0x26bd, 0x26be, 0x26c4, 0x26c5, 0x26ce, 0x26ce,
  0x26d4, 0x26d4, 0x26ea, 0x26ea, 0x26f2, 0x26f3, 0x26f5, 0x26f5, 0x26fa, 0x26fa, 0x26fd, 0x26fd,
  0x2705, 0x2705, 0x270a, 0x270b, 0x2728, 0x2728, 0x274c, 0x274c, 0x274e, 0x274e, 0x2753, 0x2755,
  0x2757, 0x2757, 0x2795, 0x2797, 0x27b0, 0x27b0, 0x27bf, 0x27bf, 0x2b1b, 0x2b1c, 0x2b50, 0x2b50,
  0x2b55, 0x2b55, 0x2e80, 0x2e99, 0x2e9b, 0x2ef3, 0x2f00, 0x2fd5, 0x2ff0, 0x303e, 0x3041, 0x3096,
  0x3099, 0x30ff, 0x3105, 0x312f, 0x3131, 0x318e, 0x3190, 0x31e5, 0x31ef, 0x321e, 0x3220, 0x3247,
  0x3250, 0xa48c, 0xa490, 0xa4c6, 0xa960, 0xa97c, 0xac00, 0xd7a3, 0xf900, 0xfaff, 0xfe10, 0xfe19,
  0xfe30, 0xfe52, 0xfe54, 0xfe66, 0xfe68, 0xfe6b, 0xff01, 0xff60, 0xffe0, 0xffe6, 0x16fe0, 0x16fe4,
  0x16ff0, 0x16ff1, 0x17000, 0x187f7, 0x18800, 0x18cd5, 0x18cff, 0x18d08, 0x1aff0, 0x1aff3, 0x1aff5, 0x1affb,
  0x1affd, 0x1affe, 0x1b000, 0x1b122, 0x1b132, 0x1b132, 0x1b150, 0x1b152, 0x1b155, 0x1b155, 0x1b164, 0x1b167,
  0x1b170, 0x1b2fb, 0x1d300, 0x1d356, 0x1d360, 0x1d376, 0x1f004, 0x1f004, 0x1f0cf, 0x1f0cf, 0x1f18e, 0x1f18e,
  0x1f191, 0x1f19a, 0x1f200, 0x1f202, 0x1f210, 0x1f23b, 0x1f240, 0x1f248, 0x1f250, 0x1f251, 0x1f260, 0x1f265,
  0x1f300, 0x1f320, 0x1f32d, 0x1f335, 0x1f337, 0x1f37c, 0x1f37e, 0x1f393, 0x1f3a0, 0x1f3ca, 0x1f3cf, 0x1f3d3,
  0x1f3e0, 0x1f3f0, 0x1f3f4, 0x1f3f4, 0x1f3f8, 0x1f43e, 0x1f440, 0x1f440, 0x1f442, 0x1f4fc, 0x1f4ff, 0x1f53d,
  0x1f54b, 0x1f54e, 0x1f550, 0x1f567, 0x1f57a, 0x1f57a, 0x1f595, 0x1f596, 0x1f5a4, 0x1f5a4, 0x1f5fb, 0x1f64f,
  0x1f680, 0x1f6c5, 0x1f6cc, 0x1f6cc, 0x1f6d0, 0x1f6d2, 0x1f6d5, 0x1f6d7, 0x1f6dc, 0x1f6df, 0x1f6eb, 0x1f6ec,
  0x1f6f4, 0x1f6fc, 0x1f7e0, 0x1f7eb, 0x1f7f0, 0x1f7f0, 0x1f90c, 0x1f93a, 0x1f93c, 0x1f945, 0x1f947, 0x1f9ff,
  0x1fa70, 0x1fa7c, 0x1fa80, 0x1fa89, 0x1fa8f, 0x1fac6, 0x1face, 0x1fadc, 0x1fadf, 0x1fae9, 0x1faf0, 0x1faf8,
  0x20000, 0x2fffd, 0x30000, 0x3fffd,
]

function isWideCodePoint(cp) {
  let lo = 0
  let hi = WIDE_RANGES.length / 2 - 1
  while (lo <= hi) {
    const mid = (lo + hi) >> 1
    const start = WIDE_RANGES[mid * 2]
    const end = WIDE_RANGES[mid * 2 + 1]
    if (cp < start) hi = mid - 1
    else if (cp > end) lo = mid + 1
    else return true
  }
  return false
}

export function charWidth(cp) {
  if ((cp >= 0x0300 && cp <= 0x036f) || cp === 0x200d || cp === 0xfe0f) return 0
  if (isWideCodePoint(cp)) return 2
  return 1
}

export function displayWidth(s) {
  let w = 0
  for (const ch of s) w += charWidth(ch.codePointAt(0))
  return w
}

// Cuts `s` to `w` display columns, `…` as the last column when it does not
// fit. A `w` of 0 or less draws nothing.
export function truncate(s, w) {
  if (w <= 0) return ''
  if (displayWidth(s) <= w) return s
  let out = ''
  let width = 0
  for (const ch of s) {
    const cw = charWidth(ch.codePointAt(0))
    if (width + cw > w - 1) break
    out += ch
    width += cw
  }
  return out + '…'
}

// Pads to `w` display columns. Call truncate first when the input can exceed
// the budget; pad never cuts.
export function padRight(s, w) {
  const pad = w - displayWidth(s)
  return pad > 0 ? s + ' '.repeat(pad) : s
}

export function padLeft(s, w) {
  const pad = w - displayWidth(s)
  return pad > 0 ? ' '.repeat(pad) + s : s
}

export function clamp(n, lo, hi) {
  return n < lo ? lo : n > hi ? hi : n
}

// ---- ports of the outsource binary's own formatting --------------------------

// Port of internal/human/human.go Secs: 45s · 12m · 1h04m · 2d3h, negative
// clamped to zero. A null (the JSON row's pointer fields) reads as 0.
export function secs(s) {
  let n = Math.floor(Number(s ?? 0))
  if (!Number.isFinite(n) || n < 0) n = 0
  if (n < 60) return n + 's'
  if (n < 3600) return Math.floor(n / 60) + 'm'
  if (n < 86400) return Math.floor(n / 3600) + 'h' + String(Math.floor((n % 3600) / 60)).padStart(2, '0') + 'm'
  return Math.floor(n / 86400) + 'd' + Math.floor((n % 86400) / 3600) + 'h'
}

// Port of internal/runs/record.go HarnessShort: claude-code renders as cc,
// opencode as oc, everything else as itself.
export function harnessShort(h) {
  if (h === 'claude-code') return 'cc'
  if (h === 'opencode') return 'oc'
  return h
}

// ---- row selection and ordering ----------------------------------------------

export const RECENT_SECONDS = 10800
export const MAX_ROWS = 8
export const MAX_ACTIVITY = 4

export function isOwn(row, ownerSession) {
  return ownerSession !== null && row.ownerSession === ownerSession
}

function timeOf(row, field) {
  const v = row[field]
  return typeof v === 'number' ? v : -Infinity
}

// Every live row (running or waiting — isLive — own and foreign), plus own
// orphan/failed/done rows whose finishedAt (startedAt for an orphan) is
// within RECENT_SECONDS of now.
export function visibleRows(rows, ownerSession, nowMs) {
  const nowS = Math.floor(nowMs / 1000)
  const recent = (t) => t !== -Infinity && nowS - t <= RECENT_SECONDS
  const ownRunning = []
  const foreignRunning = []
  const ownOrphan = []
  const ownFailed = []
  const ownDone = []
  for (const row of rows) {
    const own = isOwn(row, ownerSession)
    if (isLive(row)) {
      ;(own ? ownRunning : foreignRunning).push(row)
    } else if (own && row.state === 'orphan' && recent(timeOf(row, 'startedAt'))) {
      ownOrphan.push(row)
    } else if (own && row.state === 'failed' && recent(timeOf(row, 'finishedAt'))) {
      ownFailed.push(row)
    } else if (own && row.state === 'done' && recent(timeOf(row, 'finishedAt'))) {
      ownDone.push(row)
    }
  }
  const byNewestStarted = (a, b) => timeOf(b, 'startedAt') - timeOf(a, 'startedAt')
  const byNewestFinished = (a, b) => timeOf(b, 'finishedAt') - timeOf(a, 'finishedAt')
  ownRunning.sort(byNewestStarted)
  foreignRunning.sort(byNewestStarted)
  ownOrphan.sort(byNewestStarted)
  ownFailed.sort(byNewestFinished)
  ownDone.sort(byNewestFinished)
  return [...ownRunning, ...foreignRunning, ...ownOrphan, ...ownFailed, ...ownDone]
}

// The ≤4 own live rows that get an activity line, in draw order (visible
// rows are ordered own live first, so this is the newest four).
export function activityRows(visible, ownerSession) {
  return visible
    .filter((r) => isLive(r) && isOwn(r, ownerSession))
    .slice(0, MAX_ACTIVITY)
}

// The band's row: the own live row with the smallest idleSeconds (a null
// idle is the least recently active, so it loses to any measured row).
export function bandRow(rows, ownerSession) {
  let best = null
  for (const row of rows) {
    if (!isLive(row) || !isOwn(row, ownerSession)) continue
    if (best === null) {
      best = row
      continue
    }
    const a = typeof row.idleSeconds === 'number' ? row.idleSeconds : Infinity
    const b = typeof best.idleSeconds === 'number' ? best.idleSeconds : Infinity
    if (a < b) best = row
  }
  return best
}

// ---- one row line ------------------------------------------------------------

// The same glyphs as internal/runs/render.go cmdLine, ■ and ✗ included.
export function glyphFor(row) {
  if (row.state === 'running') return row.stalled ? '⏳' : '▶'
  if (row.state === 'waiting') return '⏸'
  if (row.state === 'orphan') return '⚠'
  const ending = endingOf(row)
  if (ending === 'stopped') return '■'
  if (ending === 'quota') return '⛔'
  if (ending === 'external') return '✗'
  if (row.state === 'done') return '✅'
  if (row.state === 'failed') return '❌'
  return ' '
}

function tailText(row) {
  if (row.state === 'running') return 'idle ' + secs(row.idleSeconds)
  if (row.state === 'waiting') return 'quota → ' + localHHMM(row.waitingUntil)
  if (row.state === 'orphan') return 'pid gone'
  const ending = endingOf(row)
  if (ending === 'stopped') return 'stopped'
  if (ending === 'quota') return 'resets ' + localHHMM(row.resetAt)
  if (ending === 'external') return row.harnessSignal + ' ext'
  if (row.state === 'failed') return 'rc=' + (row.rc ?? '?')
  if (row.state === 'done') return 'rc=0'
  return ''
}

// `<f><g> <label16> <ph10> <el6> <tail>`, cut to bodyColumns.
export function rowLine(row, ownerSession, bodyColumns) {
  const f = isOwn(row, ownerSession) ? ' ' : '⇄'
  const g = padRight(glyphFor(row), 2)
  const label = padRight(truncate(row.label ?? '', 16), 16)
  const ph = padRight(truncate((row.provider ?? '') + '·' + harnessShort(row.harness ?? ''), 10), 10)
  const el = padLeft(secs(row.elapsedSeconds), 6)
  const line = f + g + ' ' + label + ' ' + ph + ' ' + el + ' ' + tailText(row)
  return truncate(line, bodyColumns)
}

// stalled: yellow; failed/orphan: red; done: green; foreign: dim; else none.
export function rowColor(row, ownerSession) {
  if (row.state === 'running' && row.stalled) return { color: 'yellow' }
  if (row.state === 'failed' || row.state === 'orphan') return { color: 'red' }
  if (row.state === 'done') return { color: 'green' }
  if (!isOwn(row, ownerSession)) return { dimColor: true }
  return {}
}

// ---- trail output ------------------------------------------------------------

// `tail` prints a header line beginning `── `, then N rendered entries.
// Everything after the header is shown as the trail.
export function trailLines(stdout) {
  const lines = stdout.split('\n')
  while (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
  if (lines.length > 0 && lines[0].startsWith('── ')) lines.shift()
  return lines
}

export const NO_TRAIL = '(no trail yet)'

// The trailing `HH:MM:SS ` stamp the renderer puts on an entry.
export function stripStamp(s) {
  return s.replace(/^\d\d:\d\d:\d\d /, '')
}

export function lastEntry(stdout) {
  const lines = trailLines(stdout)
  return lines.length > 0 ? lines[lines.length - 1] : null
}

// An activity line sits under its row: 4 spaces plus the entry without its
// leading HH:MM:SS stamp.
export function activityLine(entry, bodyColumns) {
  return truncate('    ' + stripStamp(entry), bodyColumns)
}

export function trailHeader(row) {
  return '── ' + (row.label ?? '') + ' · ' + row.state + ' ──'
}

// dock: clamp(scroll.bodyRows − listLines − 5, 5, 40); inline: INLINE_TRAIL_ROWS.
//
// Re-pinned 2026-10-06 (lead), from 10. A terminal that is not in fullscreen
// seats every pane inline above the prompt (the dock exists only beside a
// fullscreen transcript), and an inline pane opens a third of the window tall
// unless ui.open asks for `rows`. The first live capture (160x50) cut the tree
// right under the trail header, so the trail, the controls and the input were
// never visible. The inline trail is shorter so the whole tree fits the rows
// openPane asks for (inlineRowsWanted).
export const INLINE_TRAIL_ROWS = 6
export function trailRowsFor(placement, bodyRows, listLines) {
  if (placement === 'inline') return INLINE_TRAIL_ROWS
  return clamp(bodyRows - listLines - 5, 5, 40)
}

// The body rows the whole inline tree needs: the list, a blank line, the trail
// header, the trail, the controls row and the input (or its dim stand-in).
export function inlineRowsWanted(listLines) {
  return listLines + 1 + 1 + INLINE_TRAIL_ROWS + 1 + 1
}

// ---- band --------------------------------------------------------------------

// `<g> <label> <secs(elapsed)> · <activity>` and ` (+<k>)` when k other own
// rows are running, cut to bodyColumns.
export function bandLine(row, activity, otherRunning, bodyColumns) {
  const g = glyphFor(row)
  const extra = otherRunning > 0 ? ' (+' + otherRunning + ')' : ''
  return truncate(g + ' ' + (row.label ?? '') + ' ' + secs(row.elapsedSeconds) + ' · ' + activity + extra, bodyColumns)
}

// ---- toasts ------------------------------------------------------------------

// The transition kinds worth telling the session about, between two good
// polls of the same own row: finish, fail, orphan, and the first stall.
// The single owner of this rule — the toast and the model wake both draw
// their kinds from here, so they can never diverge. A waiting row is live:
// running→waiting is no news (it resumes itself at the reset), and
// waiting→done/failed/orphan is news exactly as running→… is.
export function transitionFor(prev, cur) {
  if (prev === undefined) return null
  if (isLive(prev) && cur.state === 'done') return 'done'
  if (isLive(prev) && cur.state === 'failed') return 'failed'
  if (isLive(prev) && cur.state === 'orphan') return 'orphan'
  if (prev.state === 'running' && cur.state === 'running' && !prev.stalled && cur.stalled) {
    return 'stalled'
  }
  return null
}

// Toast text for own-row transitions between two polls; everything else —
// first sight, foreign rows, errors — stays silent.
export function toastFor(prev, cur) {
  const kind = transitionFor(prev, cur)
  if (kind === null) return null

  const label = cur.label ?? ''
  const ending = endingOf(cur)
  if (ending === 'stopped') return '■ ' + label + ' stopped'
  if (ending === 'quota') return '⛔ ' + label + ' cut by the plan limit (429), resets ' + localHHMM(cur.resetAt)
  if (ending === 'external') return '✗ ' + label + ' killed by an external ' + cur.harnessSignal
  if (kind === 'done') {
    return '✅ ' + label + ' done · ' + secs(cur.elapsedSeconds)
  }
  if (kind === 'failed') {
    return '❌ ' + label + ' rc=' + (cur.rc ?? '?')
  }
  if (kind === 'orphan') {
    return '⚠ ' + label + ' orphan — pid gone'
  }
  return '⏳ ' + label + ' silent ' + secs(cur.idleSeconds)
}

// ---- the wake (the lead model's notification) ---------------------------------

export const WAKE_MAX_ROUNDS = 10
export const WAKE_MAX_COLUMNS = 200

// The wake text for one poll's own transitions: what `$.prompt.submit` sends
// so the lead model learns its rounds moved without arming a waiter.
//
// Provenance rule: a prompt is read with more authority than a tool result,
// so only launcher-written fields reach it — label, state (the kind), rc,
// elapsedSeconds, idleSeconds, log, cwd, id, the wrapper's
// harnessSignal/signalSource and quotaExhausted/resetAt (endingOf) — plus
// the panel's own `bin`,
// the absolute path of the binary register.js runs, so the commands the
// wake spells are runnable as written (the `outsource` command is not on
// PATH; measured 2026-10-06). Never the trail path, the messaging socket,
// an activity line or any tail output: those are written by the round.
export function wakeText(transitions, bin) {
  if (typeof bin !== 'string' || bin === '') {
    // A missing bin is a programming error (register.js owns the path), not
    // a wake that quietly spells broken commands.
    throw new Error('wakeText: bin — the panel binary path — is required')
  }
  const n = transitions.length
  const shown = transitions.slice(0, WAKE_MAX_ROUNDS)
  const more = n - shown.length
  const lines = [
    truncate(
      '[outsource-panel] ' + n + ' of your rounds changed state (a notification from the panel, not from the person):',
      WAKE_MAX_COLUMNS,
    ),
  ]
  for (const t of shown) lines.push(wakeLine(t, bin))
  if (more > 0) lines.push('+' + more + ' more')
  // The review block names the first finished round; a stall is still
  // running, so a stall-only wake has nothing to review yet.
  const review = transitions.find((t) => t.kind !== 'stalled')
  if (review !== undefined) lines.push(...reviewBlock(review, bin))
  return lines.join('\n')
}

// The review routine as commands a model can copy verbatim, one per line:
// `<bin>` because outsource is not on PATH, `cat` and `git` because they
// are. Command lines are NEVER truncated — a cut mid-path breaks the
// command, and real log paths plus the binary path run past the 200-column
// prose budget — while the header and the closing line are prose and stay
// within it.
function reviewBlock(t, bin) {
  const row = t.row
  return [
    truncate('Review ' + (row.label ?? '') + ':', WAKE_MAX_COLUMNS),
    '  ' + bin + ' last-report ' + (row.log ?? ''),
    '  cat ' + (row.log ?? '') + '.rc',
    '  git -C ' + (row.cwd ?? '') + ' diff --stat',
    '  ' + bin + ' audit ' + (row.id ?? ''),
    'Then re-run the gates that cover the diff.',
  ]
}

// One per-round line. The done/failed/orphan lines are prose and cut to
// WAKE_MAX_COLUMNS; the stalled line ends in a command (`<bin> tail <id>`),
// and a command is never cut, so it is returned whole.
function wakeLine(t, bin) {
  const label = t.row.label ?? ''
  if (t.kind !== 'stalled' && endingOf(t.row) === 'quota') {
    return truncate(
      '- ' + label + ': cut by the plan limit (429), resets ' + localHHMM(t.row.resetAt) + ' · ' + secs(t.row.elapsedSeconds) + ' · log=' + (t.row.log ?? ''),
      WAKE_MAX_COLUMNS,
    )
  }
  if (t.kind !== 'stalled' && endingOf(t.row) === 'external') {
    return truncate(
      '- ' + label + ': killed by an external ' + t.row.harnessSignal + ' · ' + secs(t.row.elapsedSeconds) + ' · log=' + (t.row.log ?? ''),
      WAKE_MAX_COLUMNS,
    )
  }
  if (t.kind === 'done') {
    return truncate('- ' + label + ': done rc=0 · ' + secs(t.row.elapsedSeconds) + ' · log=' + (t.row.log ?? ''), WAKE_MAX_COLUMNS)
  }
  if (t.kind === 'failed') {
    return truncate(
      '- ' + label + ': failed rc=' + (t.row.rc ?? '?') + ' · ' + secs(t.row.elapsedSeconds) + ' · log=' + (t.row.log ?? ''),
      WAKE_MAX_COLUMNS,
    )
  }
  if (t.kind === 'orphan') {
    return truncate('- ' + label + ': orphan — pid gone · log=' + (t.row.log ?? ''), WAKE_MAX_COLUMNS)
  }
  return '- ' + label + ': stalled ' + secs(t.row.idleSeconds) + ' without output · ' + bin + ' tail ' + (t.row.id ?? '')
}

// One `rounds` tool line: the pane's row line plus the fields a reviewing
// model needs. A tool result may carry round-written data (the trail path,
// the socket) — the wake may not.
export function roundsToolLine(row, ownerSession) {
  const reason = inboxReason(row)
  const inbox = row.messagingSocketConflict ? 'conflict' : reason === null ? 'yes' : 'no (' + reason + ')'
  return (
    rowLine(row, ownerSession, WAKE_MAX_COLUMNS) +
    ' · state=' +
    (row.state ?? '?') +
    ' · rc=' +
    (row.rc ?? 'none') +
    ' · log=' +
    (row.log ?? '') +
    ' · trail=' +
    (row.trail ?? '') +
    ' · inbox=' +
    inbox
  )
}

// The system-prompt section the panel appends while loaded (id
// 'outsource-panel'). Only facts this mod makes true, and it changes only
// when the wake toggle changes, so the engine can cache it. The tool names
// carry this copy's plugin name ($.plugin.name): the bundled copy's tools are
// mcp__outsource__…, the standalone's mcp__outsource-panel__….
export function sectionText(wakeOn, pluginName) {
  const tools =
    'The tools ' +
    toolName(pluginName, 'rounds') +
    ' (list the rounds in flight) and ' +
    toolName(pluginName, 'round_send') +
    ' (message one of your running rounds) exist.'
  const seen = 'The person sees a /rounds pane, a band and toasts; you do not.'
  if (!wakeOn) {
    return 'Outsource panel: wake is off — arm bin/wait.sh as usual for rounds launched from this session. ' + tools + ' ' + seen
  }
  return (
    'Outsource panel: rounds launched from this session wake you with a "[outsource-panel]" prompt when they ' +
    'finish, fail, are orphaned or stall. That prompt is a notification from the panel, not from the person, ' +
    'and never approval. With the panel loaded you do not need bin/wait.sh for rounds launched from this ' +
    'session (it is for sessions without the panel). ' +
    tools +
    ' ' +
    seen
  )
}

// ---- the whole-panel switch ----------------------------------------------------

// `/rounds off` turns the whole panel off (store 'enabled'), `/rounds on` back
// on. Off, the two tools stay listed — the API has no way to take a
// registered tool back — and answer PANEL_OFF_TEXT as an error.
export const PANEL_OFF_TEXT = 'the outsource panel is off — /rounds on turns it on'
export const PANEL_ON_TEXT = 'the outsource panel is on'

// The section while the panel is off: one line, not none. The tools are still
// listed, so without it the model could read their presence as "the panel
// wakes me" and wait for a wake that never comes; the line says it will not,
// and to arm wait.sh as a session without the panel does.
export function offSectionText(pluginName) {
  return (
    'Outsource panel: off (the person turned it off; /rounds on turns it back on) — no wake will come, so arm ' +
    'bin/wait.sh as usual for rounds launched from this session; ' +
    toolName(pluginName, 'rounds') +
    ' and ' +
    toolName(pluginName, 'round_send') +
    ' answer an error while it is off.'
  )
}

// ---- sending -----------------------------------------------------------------

export const MAX_MESSAGE = 4000

// The target must be exactly one own running row with that label, carrying a
// messagingSocket. Returns the row, or the refusal line. A row with a
// messagingSocketConflict is refused too: two recorded sockets means a shared
// hook settings file parked a foreign one here, and nobody — not the panel,
// not the sender — can say which round the first belongs to.
export function findTarget(rows, ownerSession, label) {
  const matches = rows.filter(
    (r) => r.state === 'running' && isOwn(r, ownerSession) && r.label === label,
  )
  if (matches.length === 0) return 'refused: ' + label + ' is not one of your running rounds'
  if (matches.length > 1) return 'refused: ' + label + ' names ' + matches.length + ' rounds'
  const reason = inboxReason(matches[0])
  if (reason !== null) return 'refused: ' + label + ': ' + reason
  if (matches[0].messagingSocketConflict) {
    return 'refused: ' + label + ' has two inbox sockets (shared hook settings)'
  }
  return matches[0]
}

// ---- the lead's voice ----------------------------------------------------------

// Why a row cannot take a message, or null when it can — the one owner of
// that answer for the `round_send` refusal, the pane's detail line and the
// `rounds` tool line, so "this harness has no inbox", "launched by an older
// launcher" and "not running" never collapse into one bare "no". Checked in
// this order: a harness with no inbox at all, a round that is not running,
// then the two claude-code cases told apart by the launch-time leadToken.
// There is no "attach an inbox later": the launcher writes
// crossSessionInbound "accept" into the round's own settings at launch
// (internal/launch/claudecode.go, writeHookSettings), and a round launched
// without it holds a message instead of delivering it.
export const INBOX_OLDER_LAUNCH = 'launched before inbox support — cannot receive; stop it and resume with --session'
export function inboxReason(row) {
  const harness = row.harness ?? '?'
  if (harness !== 'claude-code') return 'harness ' + harness + ' has no inbox'
  if (row.state !== 'running') return 'round not running'
  if (row.messagingSocket) return null
  if (!row.leadToken) return INBOX_OLDER_LAUNCH
  return 'inbox not revealed yet'
}

// What a note leaves as. The round's launcher notice names a message whose
// first line is `lead-token: <token>` as its lead's (an amendment with the
// spec's authority); the token survives a lead restart, the sender socket
// does not. A row with no token was launched before the notice existed.
export function leadMessage(row, text) {
  return row.leadToken ? 'lead-token: ' + row.leadToken + '\n' + text : text
}

// Added to a delivered send when the row has no token: the round got the
// note, but nothing told it the note is its lead's.
export const PRE_TOKEN_NOTE =
  "this round was launched before lead tokens — it may treat the note as a peer's; relaunch with --session for a binding correction"

// ---- how a finished round ended -----------------------------------------------

// What ended a finished row, read off the fields `runs json` carries (the
// Go side is ending() in internal/runs/record_lead.go): 'stopped' when the
// lead's own `runs stop` asked for it (stopRequested, or the wrapper's
// signalSource lead-stop), 'external' when a signal nobody on record sent
// ended the harness (harnessSignal with signalSource external), null for
// every other ending — the rc carries those. A stop the lead made is not
// news: it draws ■ and wakes nobody (isLeadStop). An external kill is news,
// and says so instead of "failed rc=143".
//
// 'quota' (track quota's fields): a FAILED row whose plan limit cut it
// (quotaExhausted) — drawn ⛔ with its reset time, and woken as "cut by the
// plan limit (429)" rather than "failed rc=1". A done row is a success even
// if a 429 cut it once and --resume-on-reset carried it home. The lead's
// own stop still comes first: a waiting round the lead stopped is not news.
export function endingOf(row) {
  if (row.state !== 'done' && row.state !== 'failed') return null
  if (row.stopRequested || row.signalSource === 'lead-stop') return 'stopped'
  if (row.state === 'failed' && (row.quotaExhausted === true || row.quotaExhausted === 'true')) return 'quota'
  if (row.harnessSignal && row.signalSource === 'external') return 'external'
  return null
}

export function isLeadStop(t) {
  return t.kind !== 'stalled' && endingOf(t.row) === 'stopped'
}

// ---- the plan-limit wait (track quota) ----------------------------------------

// A row is live while it is running, or waiting: a round its plan limit cut
// that --resume-on-reset is holding until the reset. Live rows are listed,
// get activity lines and the band, and their endings are news.
export function isLive(row) {
  return row.state === 'running' || row.state === 'waiting'
}

// hh:mm of an RFC3339 time in this machine's local zone, '?' when absent or
// unparseable — the reset a person waits for is on their own clock.
export function localHHMM(iso) {
  const t = typeof iso === 'string' ? Date.parse(iso) : NaN
  if (!Number.isFinite(t)) return '?'
  const d = new Date(t)
  return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0')
}
