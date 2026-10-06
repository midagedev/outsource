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
  activityLine,
  activityRows,
  bandLine,
  bandRow,
  findTarget,
  inlineRowsWanted,
  isOwn,
  rowColor,
  rowLine,
  stripStamp,
  toastFor,
  trailHeader,
  trailLines,
  trailRowsFor,
  truncate,
  visibleRows,
} from './view.js'

const PANE_ID = 'outsource-rounds'
const PANE_TITLE = 'outsource rounds'
const CLOSED_MS = 5000
const OPEN_MS = 2000
const RUN_TIMEOUT_MS = 4000

// ---- module state (rebuilt on every load; session.start re-seeds it) --------

let sessionId = null
let bin = ''
let binOk = false
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

export const register = (on) => {
  on('session.start', async ($, e, next) => {
    sessionId = await $.session.id()
    const envBin = await $.env.get('OUTSOURCE_PANEL_BIN')
    const home = await $.env.get('HOME')
    bin = envBin !== undefined && envBin !== '' ? envBin : (home ?? '') + '/.claude/skills/outsource/bin/outsource'
    binOk = await $.fs.exists(bin)
    await $.command.register({
      name: 'rounds',
      description: 'Show outsource rounds',
      argumentHint: '[label] | send <label> <text>',
      immediate: true,
    })
    // A reload keeps an open pane up; the engine's record, not ours, is true.
    const panes = await $.ui.panes()
    paneOpen = panes.some((pane) => pane.id === PANE_ID)
    armTimer($, paneOpen ? OPEN_MS : CLOSED_MS)
    return next(e)
  })

  on('command.run', { command: 'rounds' }, async ($, e) => {
    const args = (e.args ?? '').trim()
    if (args === '') return togglePane($)
    if (args === 'send' || args.startsWith('send ')) {
      // The first argument `send` always means this form.
      const rest = args.slice('send'.length).trim()
      const sp = rest.indexOf(' ')
      const label = sp < 0 ? rest : rest.slice(0, sp)
      const text = sp < 0 ? '' : rest.slice(sp + 1)
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

  on('ui.close', { id: PANE_ID }, ($, e, next) => {
    paneOpen = false
    armTimer($, CLOSED_MS)
    return next(e)
  }).catch(($, e, next) => {
    // The close itself must never be refused by a failure of ours; if the hook
    // above failed before passing it on, fix the state and let the pane close.
    if (!next.called) {
      paneOpen = false
      armTimer($, CLOSED_MS)
    }
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE_ID }, ($, e) => {
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
    if (paneOpen || e.props.hasSurvey) return next(e)
    const row = bandRow(rows, sessionId)
    if (row === null) return next(e)
    const others = rows.filter((r) => r !== row && r.state === 'running' && isOwn(r, sessionId)).length
    const { Text } = $.ui.resolve(e)
    return h(Text, { dimColor: true }, bandLine(row, bandEntry ?? NO_TRAIL, others, e.props.bodyColumns))
  })
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
    out.push({ text: truncate('outsource binary not found: ' + bin, bodyColumns), textProps: { color: 'red' } })
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
  timer = $.clock.every(ms, async () => {
    // A tick is awaited (one dispatch per period, ticks never overlap) and
    // guarded: a refused period ends the interval, so one throwing tick must
    // not kill the poll loop for the rest of the session.
    try {
      await tick($)
    } catch (err) {
      try {
        $.ui.log('outsource-panel: tick failed: ' + (err instanceof Error ? err.message : String(err)))
      } catch {
        // nothing more to do; the next period retries
      }
    }
  })
}

// One tick per period; a tick still running when the next is due is skipped.
async function tick($) {
  if (tickBusy || !binOk) return
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
  toastTransitions($, rows)
  if (selectedId === null || !visible.some((row) => row.id === selectedId)) {
    selectedId = visible.length > 0 ? visible[0].id : null
  }
}

// Own-row transitions between two good polls; the first poll only seeds.
function toastTransitions($, parsedRows) {
  const own = parsedRows.filter((row) => isOwn(row, sessionId))
  if (seeded) {
    for (const row of own) {
      const toast = toastFor(prevOwn.get(row.id), row)
      if (toast !== null) $.ui.toast(toast)
    }
  }
  seeded = true
  prevOwn = new Map(own.map((row) => [row.id, { state: row.state, stalled: !!row.stalled }]))
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
  $.ui.log('→ ' + label + ': ' + text.slice(0, 80))
  const line = result.isDelivered
    ? 'sent to ' + label
    : 'not delivered to ' + label + ': ' + (result.reason ?? '')
  $.ui.toast(line)
  return line
}
