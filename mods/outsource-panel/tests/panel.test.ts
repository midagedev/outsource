// Behaviour tests for outsource-panel, run by `claude plugin test`.
//
// The engine's `$` loads the real plugin from the folder; the stubs registered
// on the test's `on` sit beneath it and stand for the host: the outsource
// binary (process.run), the session id, sends, toasts and pane placement.
// "Now" is fixed by mock.clock at NOW_MS, the instant the committed fixture's
// timestamps were built against.

import { test, expect, mock } from 'claude-code/testing'
// The loader admits only code files, so the rows reach the test through a JS
// fixture; tests/fixtures/runs.json (the shape specimen the fake binary
// serves) is pinned to it by a drift check in tests/panel-mod.test.sh.
import { ROWS } from './fixtures/runs.js'
import { charWidth, displayWidth, secs, truncate } from '../hooks/view.js'

const fixtureRows: any[] = ROWS

const OWNER = '11111111-2222-4333-8444-555555555555'
const NOW_MS = 1791300000000
const BIN = '/fakehome/.claude/skills/outsource/bin/outsource'

type Row = (typeof fixtureRows)[number]

// One rendered trail per row id the stubs serve, shaped as `outsource tail`
// prints it: a `── ` header line, then entries.
const TRAILS: Record<string, string> = {
  r01: '── api-migration · zai·claude-code · running · /tmp/t/r01.jsonl\n09:12:40 💬 Starting the API migration round.\n09:31:02 🔧 Read internal/api/routes.go\n09:58:17 🔧 Bash go test ./internal/api/...',
  r02: '── docs-sweep · zai·claude-code · running · /tmp/t/r02.jsonl\n10:58:02 💬 Reading internal/runs/cli.go.\n10:59:31 🔧 Write mods/outsource-panel/hooks/view.js\n11:00:38 💬 Width table generated from Unicode 16.',
  r03: '── gate-authoring · zai·claude-code · running · /tmp/t/r03.jsonl\n10:31:09 💬 Authoring the numeric gate.\n10:40:55 🔧 Write tests/panel-gate.test.sh',
  r04: '── harness-fix · zai·claude-code · running · /tmp/t/r04.jsonl\n10:52:20 🔧 Bash sed -n 470,500p internal/runs/cli.go\n10:55:41 💬 Row fields confirmed.',
  r05: '── quota-report · zai·claude-code · running · /tmp/t/r05.jsonl\n10:59:12 💬 Querying the plan quota endpoint.\n10:59:58 🔧 Bash bin/quota.sh\n11:00:12 🔧 Bash awk -F, quota.csv',
}

const PANE_PROPS = (bodyColumns: number, bodyRows: number) => ({
  title: 'outsource rounds',
  isFocused: false,
  bodyColumns,
  placement: 'dock' as const,
  scroll: { offset: 0, bodyRows },
  view: {},
})

const BAND_PROPS = (bodyColumns: number, hasSurvey = false) => ({
  hasSurvey,
  isWorking: false,
  maxRows: 3,
  bodyColumns,
  scroll: { offset: 0, bodyRows: 3 },
  view: {},
})

// Everything a test needs from one boot: mutable stub state and the recorders.
async function boot($: any, on: any, rows: Row[], opts: { uiOpen?: any } = {}) {
  const clock = mock.clock(on, { now: NOW_MS })
  mock.env(on, { HOME: '/fakehome', OUTSOURCE_PANEL_BIN: BIN })

  const state = {
    rows: JSON.parse(JSON.stringify(rows)) as Row[],
    runsCalls: 0,
    runsFails: false,
    runsStderr: '',
    toasts: [] as string[],
    logs: [] as string[],
    sends: [] as any[],
    sendResult: { isDelivered: true } as any,
    opens: [] as any[],
    tailNs: [] as number[],
  }

  on('session.start', () => ({ cwd: '/tmp/panel-test' })) // engine event: the result shape itself
  on('session.id', ($) => ({ value: OWNER }))
  on('command.register', ($, e: any) => ({ value: { command: e.name } }))
  on('ui.panes', () => ({ value: [] }))
  on('ui.close', () => ({ value: undefined }))
  // What the engine draws when a render hook passes on: nothing of ours.
  // (ui.invalidate stays unstubbed: the kit's own implementation is what a
  // mounted drawing follows to redraw.)
  on('ui.render', () => ({ type: 'Box', props: {}, children: [] }))
  on('fs.exists', () => ({ value: true }))
  on('ui.toast', ($, e: any) => {
    state.toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.log', ($, e: any) => {
    state.logs.push(e.text)
    return { value: undefined }
  })
  on('ui.open', ($, e: any) => {
    state.opens.push(e)
    return { value: opts.uiOpen ?? { isPlaced: true } }
  })
  on('session.send', ($, e: any) => {
    state.sends.push(e)
    return state.sendResult // engine event: the SessionSendResult itself
  })
  on('process.run', ($, e: any) => {
    const argv: string[] = e.argv
    if (argv[0] !== BIN) return { value: { exitCode: 64, stdout: '', stderr: 'unexpected binary', isStdoutTruncated: false, isStderrTruncated: false } }
    if (argv[1] === 'runs' && argv[2] === 'json') {
      state.runsCalls += 1
      if (state.runsFails) {
        return { value: { exitCode: 1, stdout: '', stderr: state.runsStderr, isStdoutTruncated: false, isStderrTruncated: false } }
      }
      return { value: { exitCode: 0, stdout: JSON.stringify(state.rows), stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
    }
    if (argv[1] === 'tail') {
      const id = argv[2]
      const n = Number(argv[argv.indexOf('-n') + 1])
      state.tailNs.push(n)
      const raw = TRAILS[id]
      if (raw === undefined) {
        return { value: { exitCode: 65, stdout: '', stderr: 'no trail yet', isStdoutTruncated: false, isStderrTruncated: false } }
      }
      const lines = raw.split('\n')
      const header = lines[0]
      const entries = lines.slice(1)
      const shown = n > 0 && entries.length > n ? entries.slice(-n) : entries
      const note = shown.length < entries.length ? [`… ${entries.length - shown.length} earlier entries not shown (-n 0 for all)`] : []
      return { value: { exitCode: 0, stdout: [header, ...note, ...shown].join('\n') + '\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
    }
    return { value: { exitCode: 64, stdout: '', stderr: 'unexpected command', isStdoutTruncated: false, isStderrTruncated: false } }
  })

  await $.session.start({ cwd: '/tmp/panel-test', surface: 'terminal', isInteractive: true })
  await clock.settle()
  return { clock, state }
}

// The Text contents of a mounted drawing, in document order.
async function texts(ui: any): Promise<string[]> {
  const found = await ui.findAll({ type: 'Text' })
  return found.map((f: any) => f.text)
}

// Every element of a mounted drawing, outermost first.
function allElements(node: any, into: any[] = []): any[] {
  if (node === null || node === undefined || typeof node !== 'object') return into
  into.push(node)
  for (const child of node.children ?? []) allElements(child, into)
  return into
}

const mountPane = ($: any, bodyColumns: number, bodyRows = 30) =>
  $.ui.mount({ plugin: 'outsource-panel', surface: 'terminal', component: 'Pane', props: PANE_PROPS(bodyColumns, bodyRows), requestId: 'outsource-rounds' })

const mountBand = ($: any, bodyColumns: number, hasSurvey = false) =>
  $.ui.mount({ plugin: 'outsource-panel', surface: 'terminal', component: 'AbovePrompt', props: BAND_PROPS(bodyColumns, hasSurvey) })

// 1. The visible set: order, the 8-row cap and `+k more`.
test('visible rows: 10 visible in contract order, 8 drawn, +2 more', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await $.command.run({ command: 'rounds', args: '' })
  await clock.advance(2000)

  const ui = await mountPane($, 120)
  const lines = await texts(ui)
  expect(lines[0].startsWith(' ▶  quota-report')).toBe(true)
  expect(lines[1].startsWith('    🔧 Bash awk')).toBe(true) // activity under its row
  expect(lines[2].startsWith(' ▶  harness-fix')).toBe(true)
  expect(lines[4].startsWith(' ▶  gate-authoring')).toBe(true)
  expect(lines[6].startsWith(' ▶  docs-sweep')).toBe(true)
  expect(lines[8].startsWith(' ⏳ api-migration')).toBe(true) // stalled, and 5th own runner: no activity line
  expect(lines[9].startsWith('⇄▶  panel-mod')).toBe(true) // foreign
  expect(lines[10].startsWith('⇄▶  inbox-bridge')).toBe(true)
  expect(lines[11].startsWith(' ⚠  old-sweep')).toBe(true) // own orphan, started 2h ago
  expect(lines[12]).toBe('+2 more')
  expect(lines[13]).toBe(' ') // the blank row that separates the list from the trail
  expect(lines[14]).toBe('── quota-report · running ──')
  expect(lines.some((l) => l.includes('hotfix'))).toBe(false) // 9th: not drawn
  expect(lines.some((l) => l.includes('tide-check'))).toBe(false) // 10th: not drawn
  expect(lines.some((l) => l.includes('stale-audit'))).toBe(false) // done 5h ago: not visible
  expect(lines.some((l) => l.includes('foreign-done'))).toBe(false) // foreign finished: not visible
  expect(lines.some((l) => l.includes('unowned-orphan'))).toBe(false) // no owner: not visible
  const hotfix = state.rows.find((r) => r.label === 'hotfix') as any
  expect(NOW_MS / 1000 - hotfix.finishedAt).toBe(1800) // the fixture pins the spec's 30 min
  await ui.unmount()
})

// 2. Every Text fits its body, at three pane widths and one band width.
test('width: every Text child fits bodyColumns', async ($, on) => {
  const { clock } = await boot($, on, fixtureRows)
  await clock.advance(5000) // one closed tick: rows + band entry
  for (const columns of [40, 72, 120]) {
    const ui = await mountPane($, columns)
    for (const text of await texts(ui)) {
      expect(displayWidth(text), `pane ${columns}: ${JSON.stringify(text)}`).toBeLessThanOrEqual(columns)
    }
    await ui.unmount()
  }
  const band = await mountBand($, 60)
  for (const text of await texts(band)) {
    expect(displayWidth(text), `band 60: ${JSON.stringify(text)}`).toBeLessThanOrEqual(60)
  }
  await band.unmount()
})

// 3. Display width: the required vectors, truncation included.
test('display width vectors and truncation', () => {
  expect(charWidth('▶'.codePointAt(0) as number)).toBe(1)
  expect(charWidth('⚠'.codePointAt(0) as number)).toBe(1)
  expect(charWidth('⇄'.codePointAt(0) as number)).toBe(1)
  expect(charWidth('⏳'.codePointAt(0) as number)).toBe(2)
  expect(charWidth('✅'.codePointAt(0) as number)).toBe(2)
  expect(charWidth('❌'.codePointAt(0) as number)).toBe(2)
  expect(charWidth('💬'.codePointAt(0) as number)).toBe(2)
  expect(charWidth('🔧'.codePointAt(0) as number)).toBe(2)
  expect(charWidth('한'.codePointAt(0) as number)).toBe(2)
  expect(charWidth('a'.codePointAt(0) as number)).toBe(1)
  expect(displayWidth('ab💬')).toBe(4)
  expect(truncate('ab💬cd', 4)).toBe('ab…')
})

// 4. secs: the port of human.Secs for the spec's inputs.
test('secs matches human.Secs', () => {
  expect(secs(0)).toBe('0s')
  expect(secs(59)).toBe('59s')
  expect(secs(60)).toBe('1m')
  expect(secs(3599)).toBe('59m')
  expect(secs(3600)).toBe('1h00m')
  expect(secs(3660)).toBe('1h01m')
  expect(secs(86399)).toBe('23h59m')
  expect(secs(86400)).toBe('1d0h')
  expect(secs(90061)).toBe('1d1h')
})

// 5. Toast seeding: first poll silent, then exactly the own transitions.
test('toasts: first poll seeds, then one toast per own transition', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await clock.advance(5000)
  expect(state.toasts).toEqual([])

  const quota = state.rows.find((r) => r.id === 'r05') as any
  quota.state = 'done'
  quota.rc = 0
  quota.finishedAt = NOW_MS / 1000 - 10
  quota.elapsedSeconds = 3665
  const foreign = state.rows.find((r) => r.id === 'r06') as any
  foreign.state = 'done'
  foreign.rc = 0
  foreign.finishedAt = NOW_MS / 1000 - 10

  await clock.advance(5000)
  expect(state.toasts).toEqual(['✅ quota-report done · 1h01m'])
})

// 6. Timer cadence: 2 polls per 10 s closed, 5 per 10 s open, back to 2 after
//    a close (so never two timers), and zero process calls from renders.
test('timer cadence and render purity', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await clock.advance(10000)
  expect(state.runsCalls).toBe(2)

  await $.command.run({ command: 'rounds', args: '' })
  await clock.advance(10000)
  expect(state.runsCalls).toBe(7)

  await $.command.run({ command: 'rounds', args: '' }) // toggle closed
  await clock.advance(10000)
  expect(state.runsCalls).toBe(9)

  const before = state.runsCalls
  const pane = await mountPane($, 120)
  await pane.redraw()
  const band = await mountBand($, 60)
  await band.redraw()
  await clock.settle()
  expect(state.runsCalls).toBe(before) // ui.render mounts run no process
  await pane.unmount()
  await band.unmount()
})

// 7. Input gating: only the own running row with a socket gets an Input.
test('input gating by messaging socket', async ($, on) => {
  const { clock } = await boot($, on, fixtureRows)
  await $.command.run({ command: 'rounds', args: '' })
  await clock.advance(2000)

  const ui = await mountPane($, 120)
  await ui.select({ key: 'round', value: 'r02' }) // docs-sweep has the socket
  await clock.settle()
  expect(await ui.find({ type: 'Input', key: 'msg' })).toBeDefined()
  expect((await ui.find({ text: 'no inbox for this round' }))?.type).toBe(undefined)

  await ui.select({ key: 'round', value: 'r05' }) // quota-report: no socket
  await clock.settle()
  expect(await ui.find({ type: 'Input', key: 'msg' })).toBeUndefined()
  expect(await ui.find({ text: 'no inbox for this round' })).toBeDefined()

  await ui.select({ key: 'round', value: 'r06' }) // foreign running
  await clock.settle()
  expect(await ui.find({ type: 'Input', key: 'msg' })).toBeUndefined()
  expect(await ui.find({ text: 'no inbox for this round' })).toBeDefined()
  await ui.unmount()
})

// 8. Sending: the shared path for the Input and /rounds send.
test('sending: success, refusals, length cap, delivery failure', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await clock.advance(5000) // one poll loads the rows the send resolves against

  const ok = await $.command.run({ command: 'rounds', args: 'send docs-sweep hello round' })
  expect(ok.text).toBe('sent to docs-sweep')
  expect(state.sends).toHaveLength(1)
  expect(state.sends[0].to).toBe('uds:/tmp/cc-socks/4242.sock')
  expect(state.sends[0].text).toBe('hello round')
  expect(state.toasts).toContain('sent to docs-sweep')
  expect(state.logs.some((l) => l.startsWith('→ docs-sweep: hello round'))).toBe(true)

  const foreign = await $.command.run({ command: 'rounds', args: 'send panel-mod hi' })
  expect(foreign.text).toBe('refused: panel-mod is not one of your running rounds')
  expect(state.sends).toHaveLength(1) // refused before any session.send

  const noInbox = await $.command.run({ command: 'rounds', args: 'send gate-authoring hi' })
  expect(noInbox.text).toBe('refused: gate-authoring has no inbox')

  const long = await $.command.run({ command: 'rounds', args: 'send docs-sweep ' + 'x'.repeat(4001) })
  expect(long.text).toBe('refused: message too long (4001 > 4000)')
  expect(state.sends).toHaveLength(1)

  state.sendResult = { isDelivered: false, reason: 'nobody home' }
  const failed = await $.command.run({ command: 'rounds', args: 'send docs-sweep try again' })
  expect(failed.text).toBe('not delivered to docs-sweep: nobody home')
  expect(state.toasts).toContain('not delivered to docs-sweep: nobody home')
})

// 9. The band: one line when it should draw, next(e) when it should not.
test('band draws exactly when it should', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await clock.advance(5000)

  const quiet = await mountBand($, 100, true)
  expect(await quiet.find({ type: 'Text', text: /quota-report/ })).toBeUndefined()
  await quiet.unmount()

  const band = await mountBand($, 100)
  const lines = await texts(band)
  expect(lines).toHaveLength(1)
  expect(lines[0].startsWith('▶ quota-report 1m · 🔧 Bash awk')).toBe(true) // smallest idleSeconds
  expect(lines[0].endsWith(' (+4)')).toBe(true) // 4 other own running rows
  await band.unmount()

  await $.command.run({ command: 'rounds', args: '' }) // pane open
  const covered = await mountBand($, 100)
  expect(await covered.find({ type: 'Text', text: /quota-report/ })).toBeUndefined()
  await covered.unmount()

  state.rows = state.rows.filter((r) => r.ownerSession !== OWNER || r.state !== 'running')
  await clock.advance(5000)
  const none = await mountBand($, 100)
  expect(await none.find({ type: 'Text', text: /quota-report|▶/ })).toBeUndefined()
  await none.unmount()
})

// 10. Errors: the red line, and the last good rows and band survive.
test('runs json failure keeps last good state', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await clock.advance(5000)
  state.runsFails = true
  state.runsStderr = 'boom: registry locked\nsecond line'
  await clock.advance(5000)

  const ui = await mountPane($, 120)
  const lines = await texts(ui)
  expect(lines[0]).toBe('runs json failed: boom: registry locked')
  expect(lines.some((l) => l.startsWith(' ▶  quota-report'))).toBe(true) // last good rows stay
  await ui.unmount()

  const band = await mountBand($, 100)
  const bandLines = await texts(band)
  expect(bandLines).toHaveLength(1) // the band keeps the last good state
  expect(bandLines[0].startsWith('▶ quota-report')).toBe(true)
  await band.unmount()
})

// 11. No focus capture: no autoFocus, no hotkey, anywhere.
test('no autoFocus or hotkey on any element', async ($, on) => {
  const { clock } = await boot($, on, fixtureRows)
  await $.command.run({ command: 'rounds', args: '' })
  await clock.advance(2000)

  const pane = await mountPane($, 120)
  const band = await mountBand($, 100)
  for (const node of [...allElements(await pane.drawn()), ...allElements(await band.drawn())]) {
    expect(node.props?.autoFocus, `${node.type} autoFocus`).toBeUndefined()
    expect(node.props?.hotkey, `${node.type} hotkey`).toBeUndefined()
  }
  await pane.unmount()
  await band.unmount()
})

// 12. The not-placed fallback: an open that cannot seat the pane answers the
// list as command text. (This build seats an asked pane at any width — the
// capture C4 could not reach this path — so it is proven here by stubbing
// the open, per UiOpenResult's isPlaced:false arm.)
test('not-placed open returns the list as text', async ($, on) => {
  const reason = 'below the 110-column dock floor (100 columns now)'
  const { clock } = await boot($, on, fixtureRows, { uiOpen: { isPlaced: false, reason } })
  await clock.advance(5000)
  const res = await $.command.run({ command: 'rounds', args: '' })
  const lines = (res.text ?? '').split('\n')
  expect(lines[0].startsWith(' ▶  quota-report')).toBe(true)
  expect(lines.some((l) => l === '+2 more')).toBe(true)
  expect(lines[lines.length - 1]).toBe('(pane not placed: ' + reason + ')')
})

// 13. Inline seating (every non-fullscreen terminal): the open asks for the rows
// the whole tree needs, and the trail fetch asks for the inline trail size, so
// the trail, the controls and the input are not folded under a third-of-window
// pane. Added 2026-10-06 by the lead after the first live capture (160x50) cut
// the tree right under the trail header. FAIL-first: without `rows` in openPane
// the first expect reads undefined; with the old inline trail of 10 the -n
// expect reads 10.
test('inline: the open asks for the whole tree and the trail fits it', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await clock.advance(5000) // one closed tick seeds the rows
  await $.command.run({ command: 'rounds', args: '' })
  // list = 8 rows + 4 activity lines + "+2 more" = 13; +1 blank +1 header +6 trail +1 controls +1 input
  expect(state.opens[0].rows).toBe(23)
  const ui = await $.ui.mount({
    plugin: 'outsource-panel',
    component: 'Pane',
    requestId: 'outsource-rounds',
    surface: 'terminal',
    viewport: { columns: 160, rows: 50 },
    props: { title: 'outsource rounds', isFocused: false, bodyColumns: 156, placement: 'inline' as const, scroll: { offset: 0, bodyRows: 23 }, view: {} },
  })
  await clock.advance(2000) // one open tick fetches the trail at the inline size
  expect(state.tailNs.includes(6)).toBe(true)
  await ui.unmount()
})

// 14. An open that comes before the first tick still sizes the pane for the
// real list: the open fetches the rows itself. FAIL-first: without the poll in
// openPane the rows request reads 10 (an empty list).
test('inline: an open before the first tick still asks for the whole tree', async ($, on) => {
  const { state } = await boot($, on, fixtureRows)
  await $.command.run({ command: 'rounds', args: '' })
  expect(state.opens[0].rows).toBe(23)
})

