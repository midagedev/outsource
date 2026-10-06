// Pure presentation rules for the outsource-panel mod. Nothing in this file
// touches `$` or any host interface: every function takes plain data and
// returns plain data, so the whole display contract is testable without a
// session (and the numbers below stay the single source of truth).
//
// The numbers are the contract given by the lead's spec:
//   label 16 columns, provider·harness 10, elapsed right-aligned 6,
//   at most 8 rows drawn with `+<k> more`, finished rounds visible for
//   10800 s, ≤4 activity lines, trail 5–40 rows (10 inline).

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

// Every running row (own and foreign), plus own orphan/failed/done rows whose
// finishedAt (startedAt for an orphan) is within RECENT_SECONDS of now.
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
    if (row.state === 'running') {
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

// The ≤4 own running rows that get an activity line, in draw order (visible
// rows are ordered own running first, so this is the newest four).
export function activityRows(visible, ownerSession) {
  return visible
    .filter((r) => r.state === 'running' && isOwn(r, ownerSession))
    .slice(0, MAX_ACTIVITY)
}

// The band's row: the own running row with the smallest idleSeconds (a null
// idle is the least recently active, so it loses to any measured row).
export function bandRow(rows, ownerSession) {
  let best = null
  for (const row of rows) {
    if (row.state !== 'running' || !isOwn(row, ownerSession)) continue
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

// The same glyphs as internal/runs/render.go cmdLine.
export function glyphFor(row) {
  if (row.state === 'running') return row.stalled ? '⏳' : '▶'
  if (row.state === 'orphan') return '⚠'
  if (row.state === 'done') return '✅'
  if (row.state === 'failed') return '❌'
  return ' '
}

function tailText(row) {
  if (row.state === 'running') return 'idle ' + secs(row.idleSeconds)
  if (row.state === 'orphan') return 'pid gone'
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

// Toast text for own-row transitions between two polls; everything else —
// first sight, foreign rows, errors — stays silent.
export function toastFor(prev, cur) {
  if (prev === undefined) return null


  const label = cur.label ?? ''
  if (prev.state === 'running' && cur.state === 'done') {
    return '✅ ' + label + ' done · ' + secs(cur.elapsedSeconds)
  }
  if (prev.state === 'running' && cur.state === 'failed') {
    return '❌ ' + label + ' rc=' + (cur.rc ?? '?')
  }
  if (prev.state === 'running' && cur.state === 'orphan') {
    return '⚠ ' + label + ' orphan — pid gone'
  }
  if (prev.state === 'running' && cur.state === 'running' && !prev.stalled && cur.stalled) {
    return '⏳ ' + label + ' silent ' + secs(cur.idleSeconds)
  }
  return null
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
  if (!matches[0].messagingSocket) return 'refused: ' + label + ' has no inbox'
  if (matches[0].messagingSocketConflict) {
    return 'refused: ' + label + ' has two inbox sockets (shared hook settings)'
  }
  return matches[0]
}
