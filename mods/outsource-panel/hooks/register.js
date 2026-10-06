// outsource-panel: shows delegated outsource rounds live in the lead session.
//
// All data comes from the outsource binary — `runs json` for the row list and
// `tail <id>` for rendered trails — which stays the single owner of registry
// parsing and transcript rendering. Every process call lives in a timer tick
// (never inside ui.render); the render hooks read module state only.
//
// The pure display rules (widths, ordering, line shapes) live in view.js.

import {
  MAX_ROWS,
  NO_TRAIL,
  PANEL_OFF_TEXT,
  PANEL_ON_TEXT,
  activityLine,
  activityRows,
  bandLine,
  bandRow,
  binCandidates,
  findTarget,
  inlineRowsWanted,
  isOwn,
  logText,
  offSectionText,
  pairVerdict,
  roundsToolLine,
  rowColor,
  rowLine,
  sectionText,
  stripStamp,
  toastFor,
  toolName,
  trailHeader,
  trailLines,
  trailRowsFor,
  transitionFor,
  truncate,
  visibleRows,
  wakeText,
} from './view.js'

const PANE_ID = 'outsource-rounds'
const PANE_TITLE = 'outsource rounds'
const CLOSED_MS = 5000
const OPEN_MS = 2000
const RUN_TIMEOUT_MS = 4000
const CATCHUP_WINDOW_MS = 24 * 3600 * 1000
const PRUNE_MS = 7 * 24 * 3600 * 1000
const REFUSE_REASON = 'the bundled outsource plugin carries the panel in this session'

// ---- module state (rebuilt on every load; session.start re-seeds it) --------

let sessionId = null
let bin = ''
let binOk = false
let binTried = [] // binCandidates in order, named when none exists
let standingDown = false // the bundled copy joined after this standalone one: every hook passes through
let enabled = true // the whole-panel switch (store 'enabled'; a missing key is on)
let bundledRoot = '' // where that bundled copy lives, for the stand-down line
let rows = [] // last good `runs json` rows
let rowsError = null // 'runs json failed: …' when the last poll failed
let visible = [] // visibleRows(rows) at the last good poll
let activities = new Map() // row id → last trail entry (stamp stripped)
let bandEntry = null // the band row's last trail entry
let trail = { id: null, lines: [] } // the selected round's last rendered trail
let paneOpen = false
let showAll = false
let selectedId = null
let paneMetrics = { bodyColumns: 80, bodyRows: 24, placement: 'dock', listLines: 0 }
let bandColumns = 80
let timer = null
let tickBusy = false
let seeded = false // the first successful poll seeds state without toasts
let prevOwn = new Map() // row id → { state, stalled } at the last good poll
let wakeOn = true // own-round transitions also wake the lead model (store 'wake')
let woken = {} // store 'woken': sessionId → roundId → the state this session has SEEN
let seen = {} // store 'seen': sessionId → ms of its last good poll
let catchupArmed = true // the first good poll runs the resume catch-up
let wakePending = false // a wake submit has not settled yet
let wakeBuffer = [] // transitions found while a submit was outstanding (already recorded)
let retryQueue = [] // { id, kind } a refused submit un-recorded, for the next poll

export const register = (on) => {
  // Never two panels. A user with the marketplace install who also passes
  // `--plugin-dir <clone>/mods/outsource-panel` loads this module twice, once
  // per plugin. The engine admits modules one at a time and every module
  // already admitted judges the next, so of the two copies exactly one judges
  // the other — and pairVerdict gives each copy both halves of the rule:
  // admitted first, the bundled copy refuses the standalone (it never loads);
  // admitted first, the standalone lets the bundled copy in and stands down.
  // Either order leaves the bundled copy alone. (A check at session.start
  // could not do this: whichever copy's session.start runs first sees nothing
  // of the other, and no API takes back a registered command or tool.)
  // Measured 2026-10-06 on 2.1.291: `--plugin-dir` folders are admitted in
  // flag order, ahead of an installed plugin, and both halves fired live. The
  // matcher spells BUNDLED_NAME and PANEL_NAME literally so the validator can
  // read it; the pair tests fail if the two ever disagree.
  on('plugin.register', { name: ['outsource', 'outsource-panel'] }, async ($, e, next) => {
    const verdict = pairVerdict($.plugin.name, e.name)
    if (verdict === 'refuse') {
      log($, 'refused the standalone ' + e.name + ' (' + e.root + '): this plugin carries the panel')
      return { refuse: REFUSE_REASON }
    }
    const res = await next(e)
    if (verdict === 'stand-down' && res.allow === true) {
      standingDown = true
      bundledRoot = e.root
    }
    return res
  }).catch(($, e, next) => {
    // A judge that fails is skipped and the module joins; answer the rule
    // here so a failure of ours never yields two panels.
    if (next.called) return next(e)
    return pairVerdict($.plugin.name, e.name) === 'refuse' ? { refuse: REFUSE_REASON } : next(e)
  })

  on('session.start', async ($, e, next) => {
    if (standingDown) {
      log($, 'standing down — the bundled outsource plugin (' + bundledRoot + ') carries the panel; this copy registers nothing')
      return next(e)
    }
    sessionId = await $.session.id()
    // First existing candidate wins: the override, this plugin's own folder
    // (root plugin), the clone around a standalone copy, install.sh's place.
    binTried = binCandidates({
      root: $.plugin.root,
      home: await $.env.get('HOME'),
      envBin: await $.env.get('OUTSOURCE_PANEL_BIN'),
    })
    bin = ''
    binOk = false
    for (const candidate of binTried) {
      if (await $.fs.exists(candidate)) {
        bin = candidate
        binOk = true
        break
      }
    }
    log($, binOk ? 'binary: ' + bin : 'binary not found; tried ' + binTried.join(', '))
    // The store survives restarts and hot reloads; the module state does not.
    enabled = (await $.store.get('enabled')) !== false
    wakeOn = (await $.store.get('wake')) !== false
    woken = asObject(await $.store.get('woken'))
    seen = asObject(await $.store.get('seen'))
    catchupArmed = true
    // A resume is a fresh process: its wake machinery starts empty; the
    // store (woken, seen, wake) is what carries over.
    wakePending = false
    wakeBuffer = []
    retryQueue = []
    // The command and the tools are registered whether the panel is on or
    // off: `/rounds on` must exist to turn it back on, and the API takes no
    // tool back, so a tool list that never changes with the switch is the
    // honest one (off, the tools answer PANEL_OFF_TEXT).
    await $.command.register({
      name: 'rounds',
      description: 'Show outsource rounds',
      argumentHint: '[label] | send <label> <text> | wake [on|off] | on | off',
      immediate: true,
    })
    // Awaited before next(e): the tools are listed by turn one. The engine
    // names them mcp__<this plugin>__<name>; the debug line records the names
    // it returned.
    const listed = await $.tool.register({
      name: 'rounds',
      description:
        'List the delegated outsource rounds this session can see: one line per round (own first, then other ' +
        "sessions' running), each with state, exit code, log, trail and inbox. Use it to check what is in flight " +
        'before launching or reviewing; "no rounds" means nothing is visible.',
      inputSchema: { type: 'object' },
    })
    const sender = await $.tool.register({
      name: 'round_send',
      description:
        'Send a mid-round correction to one of YOUR running rounds (by label); the round reads it between two ' +
        'tool calls and treats it as a spec addition. Use it when a round you launched is working from a wrong ' +
        'premise; it refuses foreign, finished or ambiguous rounds.',
      inputSchema: {
        type: 'object',
        properties: {
          label: { type: 'string', description: 'the round label, as `rounds` lists it' },
          text: { type: 'string', description: 'the correction; the round must quote it in its report' },
        },
        required: ['label', 'text'],
      },
    })
    log($, 'tools: ' + listed.tool + ', ' + sender.tool)
    // A reload keeps an open pane up; the engine's record, not ours, is true.
    const panes = await $.ui.panes()
    paneOpen = panes.some((pane) => pane.id === PANE_ID)
    if (!enabled) {
      log($, 'the panel is off (store) — /rounds on turns it on')
      if (paneOpen) {
        paneOpen = false
        await $.ui.close({ id: PANE_ID })
      }
    }
    armTimer($, paneOpen ? OPEN_MS : CLOSED_MS) // arms nothing while off
    return next(e)
  })

  on('command.run', { command: 'rounds' }, async ($, e, next) => {
    if (standingDown) return next(e)
    const args = (e.args ?? '').trim()
    // The first word, when it names a subcommand, always means the
    // subcommand — a round labelled `send`, `wake`, `on` or `off` is
    // reachable through the pane's round Select instead.
    const sp = args.indexOf(' ')
    const head = sp < 0 ? args : args.slice(0, sp)
    const rest = sp < 0 ? '' : args.slice(sp + 1).trim()
    if (head === 'on' || head === 'off') {
      if (rest !== '') return { text: 'usage: /rounds on | /rounds off' }
      return { text: await setEnabled($, head === 'on') }
    }
    if (head === 'wake') return { text: await wakeCommand($, rest) }
    // Off, everything else the command does is the panel: one line says so.
    if (!enabled) return { text: PANEL_OFF_TEXT }
    if (args === '') return togglePane($)
    if (head === 'send') {
      const labelSp = rest.indexOf(' ')
      const label = labelSp < 0 ? rest : rest.slice(0, labelSp)
      const text = labelSp < 0 ? '' : rest.slice(labelSp + 1)
      return { text: await sendToLabel($, label, text) }
    }
    // `/rounds <label>`: open the pane and select the visible row by label.
    const opened = await openPane($)
    const hit = visible.find((row) => row.label === args)
    if (hit === undefined) return { text: 'no visible round labelled ' + args }
    selectedId = hit.id
    await refreshTrail($)
    $.ui.invalidate('ui.render')
    if (!opened.isPlaced) return { text: notPlacedText(opened.reason) }
    return {}
  })

  // The model's two levers: what is in flight, and a correction into it.
  // A matcher is fixed before any `$` exists, so it names both copies' tools
  // literally; each hook answers only this copy's own name ($.plugin.name)
  // and passes the other copy's calls on.
  on('tool.call', { tool: ['mcp__outsource__rounds', 'mcp__outsource-panel__rounds'] }, ($, e, next) => {
    if (standingDown || e.tool !== toolName($.plugin.name, 'rounds')) return next(e)
    if (!enabled) return { result: PANEL_OFF_TEXT, isError: true }
    if (!binOk) return { result: 'outsource binary not found: ' + binTried.join(' · '), isError: true }
    if (visible.length === 0) return { result: 'no rounds' }
    return { result: visible.map((row) => roundsToolLine(row, sessionId)).join('\n') }
  })

  on('tool.call', { tool: ['mcp__outsource__round_send', 'mcp__outsource-panel__round_send'] }, async ($, e, next) => {
    if (standingDown || e.tool !== toolName($.plugin.name, 'round_send')) return next(e)
    if (!enabled) return { result: PANEL_OFF_TEXT, isError: true }
    // Exactly the `/rounds send` path, refusal and all.
    const line = await sendToLabel($, String(e.label ?? ''), String(e.text ?? ''))
    if (line.startsWith('sent to ')) return { result: line }
    return { result: line, isError: true }
  })

  // One session-scoped section stating only what this mod makes true. It
  // changes only when the wake toggle or the panel switch changes, so a
  // cached composition stays valid (and each toggle invalidates it).
  on('prompt.compose', ($, e, next) => {
    if (standingDown) return next(e)
    return next(e).then((base) => ({
      sections: [
        ...base.sections,
        {
          id: 'outsource-panel',
          text: enabled ? sectionText(wakeOn, $.plugin.name) : offSectionText($.plugin.name),
          scope: 'session',
        },
      ],
    }))
  })

  on('ui.close', { id: PANE_ID }, ($, e, next) => {
    if (standingDown) return next(e)
    paneOpen = false
    armTimer($, CLOSED_MS)
    return next(e)
  }).catch(($, e, next) => {
    // The close itself must never be refused by a failure of ours; if the hook
    // above failed before passing it on, fix the state and let the pane close.
    if (!next.called && !standingDown) {
      paneOpen = false
      armTimer($, CLOSED_MS)
    }
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE_ID }, ($, e, next) => {
    if (standingDown || !enabled) return next(e)
    const { Box, Text, Button, Select, Input } = $.ui.resolve(e)
    const bodyColumns = typeof e.props.bodyColumns === 'number' ? e.props.bodyColumns : 80
    const placement = e.props.placement
    const bodyRows = typeof e.props.scroll?.bodyRows === 'number' ? e.props.scroll.bodyRows : 24

    const nodes = []
    const entries = listEntries(bodyColumns)
    for (const entry of entries) nodes.push(h(Text, entry.textProps, entry.text))

    const selected = visible.find((row) => row.id === selectedId)
    if (selected !== undefined) {
      // A space, not '': an empty Text draws zero rows, and the blank row is
      // what separates the list from the selected round's trail (vision
      // verdict 2026-10-06: with no gap the trail read as more list).
      nodes.push(h(Text, {}, ' '))
      nodes.push(h(Text, {}, truncate(trailHeader(selected), bodyColumns)))
      for (const line of trail.id === selectedId ? trail.lines : []) {
        nodes.push(h(Text, {}, truncate(line, bodyColumns)))
      }
    }

    nodes.push(
      h(
        Box,
        { flexDirection: 'row', gap: 1 },
        visible.length > 0
          ? h(Select, {
              key: 'round',
              label: 'round',
              value: selectedId ?? undefined,
              options: visible.map((row) => ({ value: row.id, label: row.label ?? row.id })),
              onSelect: async (value) => {
                selectedId = value
                await refreshTrail($)
                $.ui.invalidate('ui.render')
              },
            })
          : null,
        h(Button, {
          key: 'all',
          label: showAll ? 'results+thinking: on' : 'results+thinking: off',
          onPress: async () => {
            showAll = !showAll
            await refreshTrail($)
            $.ui.invalidate('ui.render')
          },
        }),
        h(Button, {
          key: 'close',
          label: 'close',
          onPress: async () => {
            await $.ui.close({ id: PANE_ID })
          },
        }),
      ),
    )

    if (selected !== undefined && isOwn(selected, sessionId) && selected.state === 'running' && selected.messagingSocket) {
      nodes.push(
        h(Input, {
          key: 'msg',
          label: 'message → ' + truncate(selected.label ?? '', 16),
          submitLabel: 'send',
          onSubmit: async (text) => {
            await sendToLabel($, selected.label ?? '', text)
          },
        }),
      )
    } else {
      nodes.push(h(Text, { dimColor: true }, 'no inbox for this round'))
    }

    paneMetrics = { bodyColumns, bodyRows, placement, listLines: entries.length }
    return h(Box, { flexDirection: 'column' }, ...nodes)
  })

  on('ui.render', { component: 'AbovePrompt' }, ($, e, next) => {
    bandColumns = typeof e.props.bodyColumns === 'number' ? e.props.bodyColumns : 80
    if (standingDown || !enabled || paneOpen || e.props.hasSurvey) return next(e)
    const row = bandRow(rows, sessionId)
    if (row === null) return next(e)
    const others = rows.filter((r) => r !== row && r.state === 'running' && isOwn(r, sessionId)).length
    const { Text } = $.ui.resolve(e)
    return h(Text, { dimColor: true }, bandLine(row, bandEntry ?? NO_TRAIL, others, e.props.bodyColumns))
  })
}

// ---- the switches ----------------------------------------------------------------

// `/rounds off|on`: the whole panel. Off stops the timer (armTimer arms none
// while off, so not one `runs json` runs), closes the pane, drops what the
// band and the list held, and drops the wakes not yet sent (the person
// turned the panel off: nothing it held is still owed). Back on, the first
// poll re-seeds silently: `seeded` is reset, so that poll only takes the
// baseline (no toast, no live transition), and the resume catch-up is
// disarmed, so nothing that finished meanwhile wakes; that poll's markKnown
// then records every terminal own row in `woken`, so a later resume does not
// replay them either. Saying the state it is already in changes nothing.
async function setEnabled($, on) {
  if (on === enabled) return on ? PANEL_ON_TEXT : PANEL_OFF_TEXT
  enabled = on
  await $.store.set('enabled', on)
  if (on) {
    seeded = false
    catchupArmed = false
  } else {
    rows = []
    rowsError = null
    visible = []
    activities = new Map()
    bandEntry = null
    trail = { id: null, lines: [] }
    wakeBuffer = []
    retryQueue = []
    if (paneOpen) {
      paneOpen = false
      await $.ui.close({ id: PANE_ID })
    }
  }
  armTimer($, CLOSED_MS)
  $.ui.invalidate('ui.render')
  $.ui.invalidate('prompt.section')
  log($, 'panel ' + (on ? 'on' : 'off'))
  return on ? PANEL_ON_TEXT : PANEL_OFF_TEXT
}

// `/rounds wake [on|off]`: the model wake alone; independent of the panel
// switch (set while the panel is off, it applies once it is back on).
async function wakeCommand($, rest) {
  const offNote = enabled ? '' : ' (the panel itself is off — /rounds on turns it on)'
  if (rest === '') return (wakeOn ? 'wake is on — round transitions wake the lead model' : 'wake is off — toasts only') + offNote
  if (rest !== 'on' && rest !== 'off') return 'usage: /rounds wake [on|off]'
  wakeOn = rest === 'on'
  await $.store.set('wake', wakeOn)
  // The system section states the wake mode; drop the cached copy so the
  // next prompt renders the other text.
  $.ui.invalidate('prompt.section')
  log($, 'wake ' + (wakeOn ? 'on' : 'off'))
  return (wakeOn ? 'wake is on — round transitions wake the lead model' : 'wake is off — toasts only') + offNote
}

// ---- the debug log -------------------------------------------------------------

// The one place the panel writes a log line: the debug log alone, never the
// transcript, prefixed once (logText). Measured 2026-10-06: a transcript row
// read `⏺ outsource-panel: outsource-panel: no watermark for this session…`,
// the host's plugin-name prefix doubling the panel's own.
function log($, text) {
  $.ui.log(logText(text), { to: 'debug' })
}

// ---- the pane ----------------------------------------------------------------

async function openPane($) {
  // `rows` sizes the pane only when it is seated inline (the dock ignores it);
  // without it an inline pane is a third of the window and folds the trail.
  // An open before the first tick would size the pane for an empty list and
  // keep that height (measured 2026-10-06: a 10-row pane, list cut mid-way), so
  // the list is fetched first when nothing has been polled yet.
  if (!seeded && binOk) await pollRuns($)
  const rows = inlineRowsWanted(listEntries(paneMetrics.bodyColumns).length)
  const opened = await $.ui.open({ id: PANE_ID, title: PANE_TITLE, rows })
  paneOpen = true
  armTimer($, OPEN_MS)
  return opened
}

async function togglePane($) {
  if (paneOpen) {
    paneOpen = false
    armTimer($, CLOSED_MS)
    await $.ui.close({ id: PANE_ID })
    return {}
  }
  const opened = await openPane($)
  if (!opened.isPlaced) return { text: notPlacedText(opened.reason) }
  return {}
}

function notPlacedText(reason) {
  const lines = listEntries(paneMetrics.bodyColumns).map((entry) => entry.text)
  lines.push('(pane not placed: ' + reason + ')')
  return lines.join('\n')
}

// The list section: the error lines and the drawn rows with their activity
// lines and the `+<k> more` row, as drawable entries.
function listEntries(bodyColumns) {
  const out = []
  if (!binOk) {
    out.push({ text: truncate('outsource binary not found: ' + binTried.join(' · '), bodyColumns), textProps: { color: 'red' } })
    return out
  }
  if (rowsError !== null) out.push({ text: truncate(rowsError, bodyColumns), textProps: { color: 'red' } })
  const drawn = visible.slice(0, MAX_ROWS)
  const k = visible.length - drawn.length
  const actRows = activityRows(visible, sessionId)
  for (const row of drawn) {
    out.push({ text: rowLine(row, sessionId, bodyColumns), textProps: rowColor(row, sessionId) })
    if (actRows.includes(row)) {
      out.push({ text: activityLine(activities.get(row.id) ?? NO_TRAIL, bodyColumns), textProps: { dimColor: true } })
    }
  }
  if (k > 0) out.push({ text: '+' + k + ' more', textProps: { dimColor: true } })
  return out
}

// ---- polling -----------------------------------------------------------------

function armTimer($, ms) {
  if (timer !== null) timer.cancel()
  timer = null
  if (!enabled) return // off: no timer, so no poll and no process at all
  timer = $.clock.every(ms, async () => {
    // A tick is awaited (one dispatch per period, ticks never overlap) and
    // guarded: a refused period ends the interval, so one throwing tick must
    // not kill the poll loop for the rest of the session.
    try {
      await tick($)
    } catch (err) {
      try {
        log($, 'tick failed: ' + (err instanceof Error ? err.message : String(err)))
      } catch {
        // nothing more to do; the next period retries
      }
    }
  })
}

// One tick per period; a tick still running when the next is due is skipped.
async function tick($) {
  if (tickBusy || !binOk || !enabled) return
  tickBusy = true
  try {
    await pollRuns($)
    if (paneOpen) {
      await refreshTrail($)
      await refreshActivities($)
    } else {
      await refreshBand($)
    }
    $.ui.invalidate('ui.render')
  } finally {
    tickBusy = false
  }
}

async function pollRuns($) {
  let res
  try {
    res = await $.process.run([bin, 'runs', 'json'], { timeoutMs: RUN_TIMEOUT_MS })
  } catch {
    rowsError = 'runs json failed: unparseable output'
    return
  }
  if (res.exitCode !== 0) {
    const line = res.stderr.split('\n').find((l) => l.trim() !== '') ?? 'unparseable output'
    rowsError = 'runs json failed: ' + line
    return
  }
  let parsed
  try {
    parsed = JSON.parse(res.stdout)
    if (!Array.isArray(parsed)) throw new Error('not an array')
  } catch {
    rowsError = 'runs json failed: unparseable output'
    return
  }
  rows = parsed
  rowsError = null
  const nowMs = await $.clock.now()
  visible = visibleRows(rows, sessionId, nowMs)
  // Retries first (a submit that was refused last poll), then the live
  // transitions, then the resume catch-up; deliverWake dedups the union.
  const retries = takeRetries(rows)
  const transitions = ownTransitions($, rows)
  const late = catchupTransitions($, rows, nowMs)
  await deliverWake($, [...retries, ...transitions, ...late])
  markKnown(rows)
  await writeWatermark($, nowMs)
  if (selectedId === null || !visible.some((row) => row.id === selectedId)) {
    selectedId = visible.length > 0 ? visible[0].id : null
  }
}

// Own-row transitions between two good polls; the first poll only seeds.
// Returns the wake transitions and fires the toasts, which stay exactly as
// they were: one per transition, independent of the wake toggle.
function ownTransitions($, parsedRows) {
  const own = parsedRows.filter((row) => isOwn(row, sessionId))
  const out = []
  if (seeded) {
    for (const row of own) {
      const kind = transitionFor(prevOwn.get(row.id), row)
      if (kind === null) continue
      const toast = toastFor(prevOwn.get(row.id), row)
      if (toast !== null) $.ui.toast(toast)
      out.push({ row, kind })
    }
  }
  seeded = true
  prevOwn = new Map(own.map((row) => [row.id, { state: row.state, stalled: !!row.stalled }]))
  return out
}

// The first good poll after session.start: a lead that restarted while its
// rounds ran has terminal rounds it never saw. A round counts as unseen when
// this session's `woken` map has no (id, state) for it — the map is the
// "known" record, written by every good poll (markKnown), so "finished while
// I was down" and "I was there when it finished" are told apart. Orphans
// carry no finishedAt, so their anchor is startedAt. A session id with no
// watermark has never polled, so it cannot know what the lead reviewed and
// seeds silently (the first lead to load this mod resumes a session with own
// rounds it reviewed by hand — none of them may wake).
function catchupTransitions($, parsedRows, nowMs) {
  if (!catchupArmed) return []
  catchupArmed = false
  if (typeof seen[sessionId] !== 'number') {
    log($, 'no watermark for this session — seeding silently')
    return []
  }
  const delivered = asObject(woken[sessionId])
  const floorS = (nowMs - CATCHUP_WINDOW_MS) / 1000
  const out = parsedRows.filter((row) => {
    if (!isOwn(row, sessionId)) return false
    if (row.state !== 'done' && row.state !== 'failed' && row.state !== 'orphan') return false
    if (delivered[row.id] === row.state) return false
    const at = typeof row.finishedAt === 'number' ? row.finishedAt : row.startedAt
    return typeof at === 'number' && at > floorS
  })
  if (out.length > 0) {
    log(
      $,
      'resume catch-up — ' +
        out.length +
        ' own round(s) terminal and unseen (' +
        out.map((row) => row.label ?? row.id).join(', ') +
        ')',
    )
  }
  return out.map((row) => ({ row, kind: row.state }))
}

// Every good poll records each own terminal row's state into the session's
// `woken` map — seen, whether or not it was submitted and whether or not
// wake is on. This is what a later resume reads to know what the lead
// already had.
function markKnown(parsedRows) {
  const delivered = asObject(woken[sessionId])
  for (const row of parsedRows) {
    if (!isOwn(row, sessionId)) continue
    if (row.state !== 'done' && row.state !== 'failed' && row.state !== 'orphan') continue
    delivered[row.id] = row.state
  }
  woken[sessionId] = delivered
}

// One `$.prompt.submit` per poll that saw one or more undelivered own
// transitions — never one per round, never for a foreign row, never twice
// for the same (round, state). The tick NEVER awaits the submit: a prompt
// submitted while a turn runs waits for that turn, and the pane, the band
// and the toasts must keep moving exactly then. So the transitions are
// recorded as delivered (and persisted) BEFORE the submit — no later poll
// can resubmit them — and a submit that rejects un-records them for a later
// poll to retry.
async function deliverWake($, transitions) {
  const list = []
  const seenPairs = new Set()
  for (const t of transitions) {
    const key = t.row.id + ' ' + t.kind
    if (seenPairs.has(key)) continue
    seenPairs.add(key)
    list.push(t)
  }
  if (list.length === 0) return
  const delivered = asObject(woken[sessionId])
  const fresh = list.filter((t) => delivered[t.row.id] !== t.kind)
  if (fresh.length === 0) return
  for (const t of fresh) delivered[t.row.id] = t.kind
  woken[sessionId] = delivered
  await $.store.set('woken', woken)
  if (!wakeOn) return // off: toasts only; the recording above still marks them seen
  if (wakePending) {
    wakeBuffer.push(...fresh)
    log(
      $,
      'wake buffered while a submit is outstanding — ' +
        fresh.map((t) => (t.row.label ?? t.row.id) + ' → ' + t.kind).join(', '),
    )
    return
  }
  sendWake($, fresh)
}

// At most one submit outstanding: transitions that arrive while it is in
// flight are recorded and buffered, and the whole buffer leaves as one
// submit when it settles.
function sendWake($, fresh) {
  wakePending = true
  log(
    $,
    'waking the lead model — ' +
      fresh.map((t) => (t.row.label ?? t.row.id) + ' → ' + t.kind).join(', '),
  )
  void Promise.resolve($.prompt.submit({ text: wakeText(fresh, bin) }))
    .catch((err) => {
      // The prompt did not enter: it was not delivered. Un-record these so a
      // later poll can offer them again, and queue the retry.
      log(
        $,
        'wake submit failed for ' +
          fresh.map((t) => t.row.label ?? t.row.id).join(', ') +
          ': ' +
          (err instanceof Error ? err.message : String(err)),
      )
      const delivered = asObject(woken[sessionId])
      for (const t of fresh) {
        if (delivered[t.row.id] === t.kind) delete delivered[t.row.id]
        retryQueue.push({ id: t.row.id, kind: t.kind })
      }
      woken[sessionId] = delivered
      void $.store.set('woken', woken)
    })
    .then(() => {
      wakePending = false
      if (wakeBuffer.length === 0) return
      const next = wakeBuffer
      wakeBuffer = []
      if (!wakeOn) return // toggled off while outstanding: recorded, not sent
      sendWake($, next)
    })
}

// The retries a refused submit queued, resolved against the rows as they
// stand now. A row that left the registry, or no longer holds the queued
// state, has nothing left to say.
function takeRetries(currentRows) {
  if (retryQueue.length === 0) return []
  const out = []
  const queue = retryQueue
  retryQueue = []
  for (const r of queue) {
    const row = currentRows.find((x) => x.id === r.id)
    if (row !== undefined && row.state === r.kind) out.push({ row, kind: r.kind })
  }
  return out
}

// The per-session watermark, written after every good poll, and the 7-day
// prune that keeps `woken`/`seen` bounded: a session whose last poll is a
// week old (or that only ever delivered without polling) is gone.
async function writeWatermark($, nowMs) {
  seen[sessionId] = nowMs
  const floor = nowMs - PRUNE_MS
  for (const sid of Object.keys(seen)) {
    if (seen[sid] === nowMs || (typeof seen[sid] === 'number' && seen[sid] > floor)) continue
    delete seen[sid]
    if (woken[sid] !== undefined) delete woken[sid]
  }
  for (const sid of Object.keys(woken)) {
    if (seen[sid] === undefined) delete woken[sid]
  }
  await $.store.set('seen', seen)
  await $.store.set('woken', woken)
}

function asObject(v) {
  return v !== null && typeof v === 'object' && !Array.isArray(v) ? v : {}
}

async function fetchTrail($, id, n, w) {
  const argv = [bin, 'tail', id, '-n', String(n), '-w', String(Math.max(1, w))]
  if (showAll) argv.push('--all')
  let res
  try {
    res = await $.process.run(argv, { timeoutMs: RUN_TIMEOUT_MS })
  } catch {
    return [NO_TRAIL]
  }
  if (res.exitCode !== 0) return [NO_TRAIL]
  return trailLines(res.stdout)
}

async function refreshTrail($) {
  if (selectedId === null) {
    trail = { id: null, lines: [] }
    return
  }
  const n = trailRowsFor(paneMetrics.placement, paneMetrics.bodyRows, paneMetrics.listLines)
  trail = { id: selectedId, lines: await fetchTrail($, selectedId, n, paneMetrics.bodyColumns - 2) }
}

async function runTailOne($, id, w) {
  let res
  try {
    res = await $.process.run([bin, 'tail', id, '-n', '1', '-w', String(Math.max(1, w))], {
      timeoutMs: RUN_TIMEOUT_MS,
    })
  } catch {
    return NO_TRAIL
  }
  if (res.exitCode !== 0) return NO_TRAIL
  const lines = trailLines(res.stdout)
  if (lines.length === 0) return NO_TRAIL
  return stripStamp(lines[lines.length - 1])
}

async function refreshActivities($) {
  const next = new Map()
  for (const row of activityRows(visible, sessionId)) {
    next.set(row.id, await runTailOne($, row.id, paneMetrics.bodyColumns - 6))
  }
  activities = next
}

async function refreshBand($) {
  const row = bandRow(rows, sessionId)
  if (row === null) {
    bandEntry = null
    return
  }
  bandEntry = await runTailOne($, row.id, bandColumns)
}

// ---- sending -----------------------------------------------------------------

// One shared send path for the pane's Input and `/rounds send`.
async function sendToLabel($, label, text) {
  const target = findTarget(rows, sessionId, label)
  if (typeof target === 'string') return target
  if (text.length > 4000) return 'refused: message too long (' + text.length + ' > 4000)'
  let result
  try {
    result = await $.session.send({ to: 'uds:' + target.messagingSocket, text })
  } catch (err) {
    const reason = err instanceof Error ? err.message : String(err)
    result = { isDelivered: false, reason }
  }
  log($, '→ ' + label + ': ' + text.slice(0, 80))
  const line = result.isDelivered
    ? 'sent to ' + label
    : 'not delivered to ' + label + ': ' + (result.reason ?? '')
  $.ui.toast(line)
  return line
}
