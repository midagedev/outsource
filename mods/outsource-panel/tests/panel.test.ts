// Behaviour tests for outsource-panel, run by `claude plugin test`.
//
// The engine's `$` loads the real plugin from the folder; the stubs registered
// on the test's `on` sit beneath it and stand for the host: the outsource
// binary (process.run), the session id, sends, toasts and pane placement.
// "Now" is fixed by mock.clock at NOW_MS, the instant the committed fixture's
// timestamps were built against.
//
// The suite runs twice, once per copy of the panel (tests/panel-mod.test.sh):
// `claude plugin test mods/outsource-panel` loads the standalone plugin
// `outsource-panel`, `claude plugin test <repo root>` the root plugin
// `outsource` that bundles the same module. Nothing here names the plugin
// under test: boot reads it off the engine (`next.origin` of the calls the
// plugin makes) into PLUGIN, and every name the model or a mount sees is
// derived from it.

import { test, expect, mock } from 'claude-code/testing'
// The loader admits only code files, so the rows reach the test through a JS
// fixture; tests/fixtures/runs.json (the shape specimen the fake binary
// serves) is pinned to it by a drift check in tests/panel-mod.test.sh. The
// same holds for the setup pane's two binary answers (config-list, models-zen).
import { ROWS } from './fixtures/runs.js'
import { CONFIG_LIST } from './fixtures/config-list.js'
import { MODELS_ZEN } from './fixtures/models-zen.js'
import {
  BUNDLED_NAME,
  CATALOGUE_PROVIDERS,
  INBOX_OLDER_LAUNCH,
  PANEL_NAME,
  PANEL_OFF_TEXT,
  PANEL_ON_TEXT,
  PRE_TOKEN_NOTE,
  binCandidates,
  catalogueArgv,
  charWidth,
  activityRows,
  bandRow,
  defaultModelArgv,
  displayWidth,
  endingOf,
  firstLine,
  freeModelOptions,
  inboxReason,
  isLive,
  localHHMM,
  logText,
  offSectionText,
  parseConfigList,
  providerLine,
  providerStates,
  rejectionText,
  roundsToolLine,
  rowLine,
  secs,
  setupRowsWanted,
  toggleEnabledArgv,
  visibleRows,
  sectionText,
  toolName,
  truncate,
  wakeText,
} from '../hooks/view.js'

const fixtureRows: any[] = ROWS

const OWNER = '11111111-2222-4333-8444-555555555555'
const NOW_MS = 1791300000000
const BIN = '/fakehome/.claude/skills/outsource/bin/outsource'

type Row = (typeof fixtureRows)[number]

// The plugin under test, as the engine names it (boot sets it).
let PLUGIN = ''

// Every `$.ui.log` line of every test that did not go to the debug sink with
// exactly one `outsource-panel: ` prefix, judged on the line as the debug log
// writes it (measured 2026-10-06: `[<plugin>] $.ui.log (to debug): <text>`).
// The last test asserts it is empty.
const LOG_VIOLATIONS: string[] = []
function checkLog(origin: string, text: string, to: string) {
  const line = '[' + origin + '] $.ui.log (to ' + to + '): ' + text
  const prefixes = line.match(/outsource-panel: /g) ?? []
  if (to !== 'debug' || prefixes.length !== 1 || !text.startsWith('outsource-panel: ')) {
    LOG_VIOLATIONS.push(JSON.stringify({ origin, text, to }))
  }
}

// One rendered trail per row id the stubs serve, shaped as `outsource tail`
// prints it: a `── ` header line, then entries.
const TRAILS: Record<string, string> = {
  r01: '── api-migration · zai·claude-code · running · /tmp/t/r01.jsonl\n09:12:40 💬 Starting the API migration round.\n09:31:02 🔧 Read internal/api/routes.go\n09:58:17 🔧 Bash go test ./internal/api/...',
  r02: '── docs-sweep · zai·claude-code · running · /tmp/t/r02.jsonl\n10:58:02 💬 Reading internal/runs/cli.go.\n10:59:31 🔧 Write mods/outsource-panel/hooks/view.js\n11:00:38 💬 Width table generated from Unicode 16.',
  r03: '── gate-authoring · zai·claude-code · running · /tmp/t/r03.jsonl\n10:31:09 💬 Authoring the numeric gate.\n10:40:55 🔧 Write tests/panel-gate.test.sh',
  r04: '── harness-fix · zai·claude-code · running · /tmp/t/r04.jsonl\n10:52:20 🔧 Bash sed -n 470,500p internal/runs/cli.go\n10:55:41 💬 Row fields confirmed.',
  r05: '── quota-report · zai·claude-code · running · /tmp/t/r05.jsonl\n10:59:12 💬 Querying the plan quota endpoint.\n10:59:58 🔧 Bash bin/quota.sh\n11:00:12 🔧 Bash awk -F, quota.csv',
  wlab: '── wake · zai·claude-code · running · /tmp/t/wlab.jsonl\n10:59:12 💬 A round the lead happened to label wake.',
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

// `outsource config` as internal/config/cli.go answers the setup pane: `list
// --json` the one JSON line, `set`/`unset` their confirmation line (cmdSet,
// cmdUnset) — or, while the test sets one, a refusal for every write.
function configAnswer(state: any, argv: string[]) {
  const answer = (exitCode: number, stdout: string, stderr: string) => ({ exitCode, stdout, stderr, isStdoutTruncated: false, isStderrTruncated: false })
  const [, , verb, key, value] = argv
  const c = state.config
  if (verb === 'list' && argv.length === 4 && argv[3] === '--json') {
    if (state.listFails !== null) return answer(state.listFails.exitCode, '', state.listFails.stderr)
    return answer(0, (state.listStdout ?? JSON.stringify(c)) + '\n', '')
  }
  if ((verb === 'set' && argv.length === 5) || (verb === 'unset' && argv.length === 4)) {
    if (state.configRefusal !== null) return answer(state.configRefusal.exitCode, '', state.configRefusal.stderr)
    if (!(key in c.values)) return answer(64, '', 'outsource config: unknown key "' + key + '"\n')
    if (verb === 'unset') {
      if (c.values[key] === null) return answer(0, key + ' was not set (' + c.path + ')\n', '')
      c.values[key] = null
      return answer(0, key + ' unset (' + c.path + ')\n', '')
    }
    if (key.endsWith('.enabled') && value !== 'true' && value !== 'false') {
      return answer(64, '', 'outsource config: ' + key + ' takes true or false, got: ' + value + '\n')
    }
    c.values[key] = key.endsWith('.enabled') ? value === 'true' : value
    return answer(0, key + ' = ' + value + ' (' + c.path + ')\n', '')
  }
  return answer(64, '', 'outsource config: unexpected ' + argv.slice(1).join(' ') + '\n')
}

// `outsource models --provider <p> … --json`: the provider's stdout, exit 1
// when the test serves none (no catalogue loaded), a `{ deny }` standing for a
// timeout (it rejects the plugin's call; a stub that throws would be skipped
// and the real run would answer); held while modelsHeld is an array.
function modelsAnswer(state: any, argv: string[]) {
  const provider = argv[argv.indexOf('--provider') + 1]
  const answer = () => {
    if (state.modelsReject !== null) return { deny: state.modelsReject }
    const stdout = state.models[provider]
    if (stdout === undefined) {
      return { value: { exitCode: 1, stdout: '', stderr: 'models: ' + provider + ': no catalogue loaded\n', isStdoutTruncated: false, isStderrTruncated: false } }
    }
    return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  }
  if (state.modelsHeld === null) return answer()
  return new Promise((resolve) => state.modelsHeld.push(() => resolve(answer())))
}

// Everything a test needs from one boot: mutable stub state and the recorders.
// `env` replaces the environment the plugin reads; `exists` answers $.fs.exists
// (every path exists when left out).
async function boot(
  $: any,
  on: any,
  rows: Row[],
  opts: {
    uiOpen?: any
    store?: Record<string, unknown>
    env?: Record<string, string>
    exists?: (path: string) => boolean
    panes?: Array<{ id: string }>
  } = {},
) {
  const clock = mock.clock(on, { now: NOW_MS })
  mock.env(on, opts.env ?? { HOME: '/fakehome', OUTSOURCE_PANEL_BIN: BIN })

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
    prompts: [] as string[],
    tools: [] as string[],
    registered: [] as string[], // full tool names, as the engine returns them
    commands: [] as string[], // `<plugin>:<command>` per command.register
    logEntries: [] as Array<{ origin: string; text: string; to: string }>,
    existsAsked: [] as string[],
    processCalls: 0, // every $.process.run, runs json and tail alike
    closes: [] as any[],
    bin: BIN, // the binary process.run answers for
    store: JSON.parse(JSON.stringify(opts.store ?? {})) as Record<string, unknown>,
    // The gate the engine's prompt intake stands behind: 'resolve' (default)
    // enters at once; 'never' hangs the submit (defect 1's frozen-tick
    // scenario); submitFails rejects the first N; submitDefers holds each
    // submit for the test to release.
    submitMode: 'resolve' as 'resolve' | 'never',
    submitFails: 0,
    submitDefers: null as null | Array<{ promise: Promise<any>; resolve: (v: any) => void }>,
    // The setup pane's binary: every process.run argv and init in order, the
    // config the `config` verbs answer from and write to, and the `models`
    // answers. A refusal answers every write while set; `models` holds its
    // answer while modelsHeld is an array (each entry releases one).
    argvs: [] as string[][],
    inits: [] as any[],
    hints: [] as string[], // argumentHint per command.register
    config: JSON.parse(JSON.stringify(CONFIG_LIST)) as { path: string; values: Record<string, unknown>; unknown: string[] },
    configRefusal: null as null | { exitCode: number; stderr: string },
    listFails: null as null | { exitCode: number; stderr: string },
    listStdout: null as null | string, // a list answer that replaces the config's
    models: { zen: JSON.stringify(MODELS_ZEN) } as Record<string, string>, // stdout by provider; absent: exit 1
    modelsReject: null as null | string,
    modelsHeld: null as null | Array<() => void>,
  }

  on('session.start', () => ({ cwd: '/tmp/panel-test' })) // engine event: the result shape itself
  // The panel's first call at session.start: whoever makes it is the plugin
  // under test (a stand-in plugin never asks).
  on('session.id', ($, e: any, next: any) => {
    PLUGIN = next.origin.plugin
    return { value: OWNER }
  })
  on('command.register', ($, e: any, next: any) => {
    state.commands.push(next.origin.plugin + ':' + e.name)
    state.hints.push(e.argumentHint)
    return { value: { command: e.name } }
  })
  // The engine names a plugin's tool mcp__<plugin>__<name>; so does this stub,
  // from the plugin that registered it.
  on('tool.register', ($, e: any, next: any) => {
    state.tools.push(e.name)
    const tool = toolName(next.origin.plugin, e.name)
    state.registered.push(tool)
    return { value: { tool } }
  })
  on('ui.panes', () => ({ value: opts.panes ?? [] }))
  on('ui.close', ($, e: any) => {
    state.closes.push(e)
    return { value: undefined }
  })
  // What the engine draws when a render hook passes on: nothing of ours.
  // (ui.invalidate stays unstubbed: the kit's own implementation is what a
  // mounted drawing follows to redraw.)
  on('ui.render', () => ({ type: 'Box', props: {}, children: [] }))
  on('fs.exists', ($, e: any) => {
    state.existsAsked.push(e.path)
    return { value: opts.exists === undefined ? true : opts.exists(e.path) }
  })
  on('ui.toast', ($, e: any) => {
    state.toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.log', ($, e: any, next: any) => {
    state.logs.push(e.text)
    state.logEntries.push({ origin: next.origin.plugin, text: e.text, to: e.to })
    checkLog(next.origin.plugin, e.text, e.to)
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
  // A plugin-submitted prompt, as the engine would take it: recorded, and
  // entered (the call resolves to the prompt that entered) — or gated, per
  // the submit gate above.
  on('prompt.submit', ($, e: any) => {
    state.prompts.push(e.text)
    if (state.submitMode === 'never') return new Promise<never>(() => undefined)
    if (state.submitFails > 0) {
      state.submitFails -= 1
      return Promise.reject(new Error('gate: submit refused'))
    }
    if (state.submitDefers !== null) {
      let resolve!: (v: any) => void
      const promise = new Promise<any>((r) => {
        resolve = r
      })
      state.submitDefers.push({ promise, resolve })
      return promise
    }
    return { text: e.text, origin: e.origin }
  })
  // The plugin's own store, as the engine keeps it: JSON values, cloned on
  // write so a later in-memory mutation cannot reach back into it.
  on('store.get', ($, e: any) => ({ value: state.store[e.key] }))
  on('store.set', ($, e: any) => {
    state.store[e.key] = JSON.parse(JSON.stringify(e.value))
    return { value: undefined }
  })
  on('store.delete', ($, e: any) => {
    delete state.store[e.key]
    return { value: undefined }
  })
  on('store.keys', () => ({ value: Object.keys(state.store) }))
  // The engine's own composition is not served in a test (measured: the
  // plugin's compose hook was skipped with a bottom error), so the host
  // answers with an empty base the plugin appends to.
  on('prompt.compose', () => ({ sections: [] }))
  on('process.run', ($, e: any) => {
    state.processCalls += 1
    const argv: string[] = e.argv
    state.argvs.push([...argv])
    state.inits.push(e.init)
    if (argv[0] !== state.bin) return { value: { exitCode: 64, stdout: '', stderr: 'unexpected binary', isStdoutTruncated: false, isStderrTruncated: false } }
    if (argv[1] === 'config') return { value: configAnswer(state, argv) }
    if (argv[1] === 'models') return modelsAnswer(state, argv)
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
  $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'Pane', props: PANE_PROPS(bodyColumns, bodyRows), requestId: 'outsource-rounds' })

const mountBand = ($: any, bodyColumns: number, hasSurvey = false) =>
  $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'AbovePrompt', props: BAND_PROPS(bodyColumns, hasSurvey) })

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

  // Re-pinned 2026-10-06 (voice round), from 'no inbox for this round': the
  // line now gives the reason (inboxReason), which is the point of the
  // change — "no inbox" alone could not tell an older launch from a harness
  // with no inbox or a finished round. Both rows are claude-code, running,
  // with neither a socket nor a lead token: launched before inbox support.
  await ui.select({ key: 'round', value: 'r05' }) // quota-report: no socket
  await clock.settle()
  expect(await ui.find({ type: 'Input', key: 'msg' })).toBeUndefined()
  expect(await ui.find({ text: 'no inbox: ' + INBOX_OLDER_LAUNCH })).toBeDefined()

  await ui.select({ key: 'round', value: 'r06' }) // foreign running
  await clock.settle()
  expect(await ui.find({ type: 'Input', key: 'msg' })).toBeUndefined()
  expect(await ui.find({ text: 'no inbox: ' + INBOX_OLDER_LAUNCH })).toBeDefined()
  await ui.unmount()
})

// 8. Sending: the shared path for the Input and /rounds send.
test('sending: success, refusals, length cap, delivery failure', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await clock.advance(5000) // one poll loads the rows the send resolves against

  // Re-pinned 2026-10-06 (voice round), from 'sent to docs-sweep': the
  // fixture row carries no lead token (an older launch), so the send goes out
  // as before — the text unchanged — and the result now says the round may
  // read it as a peer's (PRE_TOKEN_NOTE). The token case is 'round_send
  // prefixes the lead token' below.
  const ok = await $.command.run({ command: 'rounds', args: 'send docs-sweep hello round' })
  expect(ok.text).toBe('sent to docs-sweep · ' + PRE_TOKEN_NOTE)
  expect(state.sends).toHaveLength(1)
  expect(state.sends[0].to).toBe('uds:/tmp/cc-socks/4242.sock')
  expect(state.sends[0].text).toBe('hello round')
  expect(state.toasts).toContain('sent to docs-sweep · ' + PRE_TOKEN_NOTE)
  // Updated 2026-10-06 (bundle round), from startsWith('→ docs-sweep: …'): the
  // line now goes through logText like every other panel log line, which
  // leads it with the one `outsource-panel: ` prefix in both copies.
  expect(state.logs).toContain(logText('→ docs-sweep: hello round'))

  const foreign = await $.command.run({ command: 'rounds', args: 'send panel-mod hi' })
  expect(foreign.text).toBe('refused: panel-mod is not one of your running rounds')
  expect(state.sends).toHaveLength(1) // refused before any session.send

  // Re-pinned 2026-10-06 (voice round), from 'refused: gate-authoring has no
  // inbox': the refusal names the reason (inboxReason).
  const noInbox = await $.command.run({ command: 'rounds', args: 'send gate-authoring hi' })
  expect(noInbox.text).toBe('refused: gate-authoring: ' + INBOX_OLDER_LAUNCH)

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
    plugin: PLUGIN,
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

// 15. An ambiguous inbox: a row carrying messagingSocketConflict (a second,
// foreign socket parked by a shared hook settings file) must be refused with
// no send — the first socket may belong to a different round, so a send that
// goes through is a silent wrong delivery. The fixture stays shared (capture
// checks pin it), so the conflict row is built inline from r02, the own
// running row with a socket. FAIL-first: without the refusal the send goes
// through and res.text reads 'sent to docs-sweep'.
test('send refuses a row with two inbox sockets', async ($, on) => {
  const rows = fixtureRows.map((r: any) =>
    r.id === 'r02' ? { ...r, messagingSocketConflict: '/tmp/cc-socks/9999.sock' } : r,
  )
  const { clock, state } = await boot($, on, rows)
  await clock.advance(5000) // one poll loads the rows the send resolves against

  const res = await $.command.run({ command: 'rounds', args: 'send docs-sweep which one' })
  expect(res.text).toBe('refused: docs-sweep has two inbox sockets (shared hook settings)')
  expect(state.sends).toHaveLength(0) // refused before any session.send
})

// ---- the wake: the lead model's notification ---------------------------------
//
// The rows below are built inline: the shared fixture is pinned by the
// capture checks, and the wake needs rows in states the fixture never holds
// (an own round flipping mid-test).

const OTHER = '99999999-8888-7777-6666-555555555555'

const wakeRow = (over: Partial<Row> = {}): Row =>
  ({
    id: 'w1',
    pid: 5001,
    label: 'api-fix',
    provider: 'zai',
    harness: 'claude-code',
    model: 'glm-5.3',
    cwd: '/tmp/panel-fixtures/wt-w1',
    spec: '/tmp/panel-fixtures/specs/w1.md',
    log: '/tmp/panel-fixtures/logs/w1.log',
    progressDir: '/tmp/panel-fixtures/progress/w1',
    trail: '/tmp/panel-fixtures/trails/w1.jsonl',
    trailFormat: 'claude-transcript',
    ownerSession: OWNER,
    ownerClaudePid: '41000',
    startedAt: NOW_MS / 1000 - 600,
    rc: null,
    finishedAt: null,
    session: 'dddd4444-0000-4000-8000-000000000101',
    modelActual: 'glm-5.3',
    state: 'running',
    elapsedSeconds: 600,
    idleSeconds: 5,
    stalled: false,
    ...over,
  }) as Row

// A hot reload, simulated at the one boundary that matters: session.start
// re-fired through the engine, so the plugin re-reads the store (wake, woken,
// seen) and re-arms the resume catch-up against the store the first
// incarnation left — the module's own seeding state aside, exactly what a
// fresh module does on a real reload.
const reRegister = ($: any) =>
  $.session.start({ cwd: '/tmp/panel-test', surface: 'terminal', isInteractive: true })

const flipDone = (row: any, atSec = 10) => {
  row.state = 'done'
  row.rc = 0
  row.pid = null
  row.finishedAt = NOW_MS / 1000 - atSec
  row.elapsedSeconds = 3665
  row.idleSeconds = null
}

// 16. The wake text contract: header naming n, one line per transition, the
// review block — one command per line, spelled with the binary's absolute
// path because `outsource` is not on PATH — only when something finished, the
// 10-row cap with +N more. FAIL-first: without the cap the 12-row case draws
// 12 rows and no '+2 more'; without `bin` the review commands come out wrong
// (or the call throws).
test('wakeText matches the contract', () => {
  const done = wakeRow({ id: 'w1', label: 'api-fix', state: 'done', rc: 0, finishedAt: NOW_MS / 1000 - 10, elapsedSeconds: 3665, idleSeconds: null })
  const failed = wakeRow({ id: 'w2', label: 'web-polish', state: 'failed', rc: 2, finishedAt: NOW_MS / 1000 - 30, elapsedSeconds: 120, idleSeconds: null, log: '/tmp/panel-fixtures/logs/w2.log' })
  const orphan = wakeRow({ id: 'w3', label: 'night-sweep', state: 'orphan', pid: null, startedAt: NOW_MS / 1000 - 7200, elapsedSeconds: 7200, idleSeconds: null, log: '/tmp/panel-fixtures/logs/w3.log' })
  const lines = wakeText(
    [
      { row: done, kind: 'done' },
      { row: failed, kind: 'failed' },
      { row: orphan, kind: 'orphan' },
    ],
    BIN,
  ).split('\n')
  expect(lines).toEqual([
    '[outsource-panel] 3 of your rounds changed state (a notification from the panel, not from the person):',
    '- api-fix: done rc=0 · 1h01m · log=/tmp/panel-fixtures/logs/w1.log',
    '- web-polish: failed rc=2 · 2m · log=/tmp/panel-fixtures/logs/w2.log',
    '- night-sweep: orphan — pid gone · log=/tmp/panel-fixtures/logs/w3.log',
    'Review api-fix:',
    '  ' + BIN + ' last-report /tmp/panel-fixtures/logs/w1.log',
    '  cat /tmp/panel-fixtures/logs/w1.log.rc',
    '  git -C /tmp/panel-fixtures/wt-w1 diff --stat',
    '  ' + BIN + ' audit w1',
    'Then re-run the gates that cover the diff.',
  ])

  // A missing bin is a programming error (register.js owns the path), not a
  // wake that quietly spells broken commands.
  expect(() => wakeText([{ row: done, kind: 'done' }])).toThrow()

  // A stall is still running: no review block; its own command (`<bin> tail
  // <id>`) is spelled with the binary's path too.
  const stalled = wakeRow({ label: 'api-fix', stalled: true, idleSeconds: 720 })
  expect(wakeText([{ row: stalled, kind: 'stalled' }], BIN).split('\n')).toEqual([
    '[outsource-panel] 1 of your rounds changed state (a notification from the panel, not from the person):',
    '- api-fix: stalled 12m without output · ' + BIN + ' tail w1',
  ])

  const many = Array.from({ length: 12 }, (_, i) => ({
    row: wakeRow({ id: 'm' + i, label: 'row-' + i, state: 'done', rc: 0, finishedAt: NOW_MS / 1000 - (i + 1), elapsedSeconds: 60, idleSeconds: null, log: '/tmp/panel-fixtures/logs/m' + i + '.log' }),
    kind: 'done' as const,
  }))
  const manyLines = wakeText(many, BIN).split('\n')
  expect(manyLines).toHaveLength(18) // header + 10 rows + "+2 more" + 6 review lines
  expect(manyLines[11]).toBe('+2 more')
  expect(manyLines[0]).toContain('12 of your rounds')
  expect(manyLines[12]).toBe('Review row-0:') // the first finished round, as ever
})

// 16b. Command lines are never truncated, however long the paths: with a
// real 51-character binary path and a 140-character log path the
// last-report line runs past the 200-column prose budget and must still
// arrive whole — a model copies wake lines verbatim, and a mid-path `…`
// breaks the command. Everything that is not a command is prose and stays
// within the budget. FAIL-first: against a truncate-everything wake the
// command lines come back cut (a `…` where the path continued).
test('wake commands arrive whole, prose stays within 200 columns', () => {
  const BIN51 = '/Users/exmpl/.claude/skills/outsource/bin/outsource'
  expect(BIN51.length).toBe(51)
  const longLog = '/tmp/panel-fixtures/logs/' + 'x'.repeat(111) + '.log'
  expect(longLog.length).toBe(140)

  const done = wakeRow({ id: 'w1', label: 'api-fix', state: 'done', rc: 0, finishedAt: NOW_MS / 1000 - 10, elapsedSeconds: 3665, idleSeconds: null, log: longLog })
  const lines = wakeText([{ row: done, kind: 'done' }], BIN51).split('\n')
  const commands = [
    '  ' + BIN51 + ' last-report ' + longLog,
    '  cat ' + longLog + '.rc',
    '  git -C /tmp/panel-fixtures/wt-w1 diff --stat',
    '  ' + BIN51 + ' audit w1',
  ]
  expect(displayWidth(commands[0])).toBeGreaterThan(200) // the case the prose budget cannot hold
  for (const cmd of commands) expect(lines).toContain(cmd) // whole: equal to the expected string

  // The stall line carries a command too, so a long label cannot get it cut.
  const stalled = wakeRow({ id: 'w1', label: 's'.repeat(160), stalled: true, idleSeconds: 720 })
  const stallLines = wakeText([{ row: stalled, kind: 'stalled' }], BIN51).split('\n')
  const stallLine = '- ' + 's'.repeat(160) + ': stalled 12m without output · ' + BIN51 + ' tail w1'
  expect(displayWidth(stallLine)).toBeGreaterThan(200)
  expect(stallLines).toContain(stallLine)

  const commandSet = new Set([...commands, stallLine])
  for (const line of [...lines, ...stallLines]) {
    if (commandSet.has(line)) continue
    expect(displayWidth(line), JSON.stringify(line)).toBeLessThanOrEqual(200)
  }
})

// 17. One $.prompt.submit per poll that saw transitions — never one per
// round — and never again for the same (round, state).
test('wake: exactly one submit per transitioning poll, none after', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow()])
  await clock.advance(5000) // poll 1 seeds
  expect(state.prompts).toEqual([])

  const done = state.rows.find((r) => r.id === 'w1') as any
  flipDone(done)
  await clock.advance(5000) // poll 2: the running→done transition
  expect(state.prompts).toHaveLength(1)
  expect(state.prompts[0]).toBe(wakeText([{ row: done, kind: 'done' }], BIN))
  expect(state.toasts).toEqual(['✅ api-fix done · 1h01m']) // the toast still fires
  expect(state.store['woken']).toEqual({ [OWNER]: { w1: 'done' } })

  await clock.advance(5000) // poll 3: the row is still done
  expect(state.prompts).toHaveLength(1)
  expect(state.store['seen']).toEqual({ [OWNER]: NOW_MS + 15000 })
})

// 18. A foreign row's transition wakes nobody — not the toast, not the model.
test('wake: a foreign row stays silent', async ($, on) => {
  const { clock, state } = await boot($, on, [
    wakeRow({ id: 'f1', label: 'panel-mod', ownerSession: OTHER }),
  ])
  await clock.advance(5000)
  flipDone(state.rows.find((r) => r.id === 'f1') as any)
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
  expect(state.toasts).toEqual([])
})

// 19. A fresh session id seeds silently: with no watermark for this session,
// the panel cannot know which finished rounds the lead already reviewed, so
// none of them may wake (the first lead to load this mod resumes a session
// with own rounds it reviewed by hand). The first poll still records every
// terminal own row as seen, so a LATER resume stays silent too.
test('wake: a fresh session with own done rows seeds silently', async ($, on) => {
  const rows = ['d1', 'd2', 'd3'].map((id, i) =>
    wakeRow({ id, label: 'done-' + id, state: 'done', rc: 0, pid: null, finishedAt: NOW_MS / 1000 - 60 * (i + 1), elapsedSeconds: 300, idleSeconds: null }),
  )
  const { clock, state } = await boot($, on, rows)
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
  expect(state.store['seen']).toEqual({ [OWNER]: NOW_MS + 5000 }) // seeded, silently
  expect(state.store['woken']).toEqual({
    [OWNER]: { d1: 'done', d2: 'done', d3: 'done' }, // seen, never submitted
  })
})

// 20. Resume catch-up: a round that finished after this session's last poll
// (the lead restarted while it ran) wakes once, at the first poll back.
test('wake: resume catch-up wakes for finished-after-watermark rounds', async ($, on) => {
  const row = wakeRow({ id: 'late1', label: 'late-fix' })
  flipDone(row as any, 3600) // finished 1 h ago
  const { clock, state } = await boot($, on, [row], {
    store: { seen: { [OWNER]: NOW_MS - 2 * 3600 * 1000 } }, // last polled 2 h ago
  })
  await clock.advance(5000)
  expect(state.prompts).toHaveLength(1)
  expect(state.prompts[0]).toBe(wakeText([{ row: state.rows[0] as any, kind: 'done' }], BIN))
  expect(state.store['woken']).toEqual({ [OWNER]: { late1: 'done' } })
  await clock.advance(5000) // one wake, not one per poll
  expect(state.prompts).toHaveLength(1)
})

// 21. The same row already delivered (in `woken`) never wakes again — a hot
// reload, a second poll, a resume.
test('wake: an already-woken round stays silent after a reload', async ($, on) => {
  const row = wakeRow({ id: 'late1', label: 'late-fix' })
  flipDone(row as any, 3600)
  const { clock, state } = await boot($, on, [row], {
    store: { seen: { [OWNER]: NOW_MS - 2 * 3600 * 1000 }, woken: { [OWNER]: { late1: 'done' } } },
  })
  await reRegister($) // fresh module state, the store intact
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
})

// 22. A terminal round older than the catch-up window stays silent. The
// premise of this test was the old `finishedAt > watermark` rule (a 3 h-old
// round against a 2 h-old watermark); the delivered rule is "not in woken,
// within 24 h", under which that 3 h-old unseen round correctly WAKES — the
// session has no record of ever seeing it. The bound that keeps silence now
// is the 24 h window, so that is what this test pins. No assertion was
// relaxed: silence is still asserted, for the rule actually shipped.
test('wake: a terminal round older than the catch-up window stays silent', async ($, on) => {
  const row = wakeRow({ id: 'old1', label: 'old-fix' })
  flipDone(row as any, 25 * 3600) // finished 25 h ago: outside the window
  const { clock, state } = await boot($, on, [row], {
    store: { seen: { [OWNER]: NOW_MS - 2 * 3600 * 1000 } },
  })
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
  expect(state.store['woken']).toEqual({ [OWNER]: { old1: 'done' } }) // recorded as seen, not woken
})

// 23. A hot reload with the store intact never re-wakes what was delivered
// before it (the live path delivered it; the reload re-seeds).
test('wake: a hot reload after a delivered transition wakes nobody again', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow()])
  await clock.advance(5000) // poll 1: seed + watermark
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000) // poll 2: the transition, one wake
  expect(state.prompts).toHaveLength(1)

  await reRegister($) // module state fresh, store (woken, seen) intact
  await clock.advance(5000)
  await clock.advance(5000)
  expect(state.prompts).toHaveLength(1)
})

// 24. The toggle: off means toasts only; the setting persists through a
// restart (the store); the subcommand words beat round labels; usage errors
// select nothing; a round labelled `wake` stays reachable in the pane.
test('rounds wake toggle: off, persistence, subcommand priority', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow(), wakeRow({ id: 'wlab', label: 'wake', log: '/tmp/panel-fixtures/logs/wlab.log' })])
  await clock.advance(5000)

  const bare = await $.command.run({ command: 'rounds', args: 'wake' })
  expect(bare.text).toBe('wake is on — round transitions wake the lead model')

  const off = await $.command.run({ command: 'rounds', args: 'wake off' })
  expect(off.text).toBe('wake is off — toasts only')
  expect(state.store['wake']).toBe(false)

  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000)
  expect(state.toasts).toEqual(['✅ api-fix done · 1h01m']) // toasts either way
  expect(state.prompts).toEqual([]) // no wake while off

  const back = await $.command.run({ command: 'rounds', args: 'wake on' })
  expect(back.text).toBe('wake is on — round transitions wake the lead model')
  expect(state.store['wake']).toBe(true)

  // `wake` takes exactly on/off/nothing: anything else is usage, and never a
  // label lookup (no pane open, no selection).
  const opensBefore = state.opens.length
  const usage = await $.command.run({ command: 'rounds', args: 'wake maybe' })
  expect(usage.text).toBe('usage: /rounds wake [on|off]')
  expect(state.opens.length).toBe(opensBefore)

  // A round actually labelled `wake` is still selectable in the pane.
  await $.command.run({ command: 'rounds', args: '' })
  const ui = await mountPane($, 120)
  await ui.select({ key: 'round', value: 'wlab' })
  await clock.settle()
  expect(await ui.find({ text: '── wake · running ──' })).toBeDefined()
  await ui.unmount()
})

// 25. The toggle survives a restart because it lives in the store, not the
// module: a session that boots with wake off never submits.
test('wake off persists into a new session', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow()], { store: { wake: false } })
  await clock.advance(5000)
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000)
  expect(state.toasts).toEqual(['✅ api-fix done · 1h01m'])
  expect(state.prompts).toEqual([])
})

// 26. Provenance: a prompt is read with more authority than a tool result,
// so round-written data (the trail, the socket, the activity text) may reach
// the `rounds` tool but never the wake text.
test('wake text never carries round-written fields', async ($, on) => {
  const marked = wakeRow({
    id: 'w9',
    label: 'marker-run',
    trail: '/tmp/MARKER9-trails/w9.jsonl',
    messagingSocket: '/tmp/MARKER9-socks/w9.sock',
  })
  const { clock, state } = await boot($, on, [marked])
  await $.command.run({ command: 'rounds', args: '' }) // open the pane: activities fetch the trail
  await clock.advance(5000)
  flipDone(state.rows.find((r) => r.id === 'w9') as any)
  await clock.advance(5000)
  expect(state.prompts).toHaveLength(1)
  expect(state.prompts[0].includes('MARKER9')).toBe(false)
  // Teeth: the same row through the tool surface does carry them.
  const line = roundsToolLine(state.rows.find((r) => r.id === 'w9') as any, OWNER)
  expect(line.includes('MARKER9')).toBe(true)
})

// 27. The model's tools: `rounds` lists the visible rows with the fields a
// review needs; `round_send` is exactly the /rounds send path, and a refusal
// is a tool error.
test('rounds and round_send tools', async ($, on) => {
  const rows = [
    wakeRow({ id: 's1', label: 'docs-sweep', messagingSocket: '/tmp/cc-socks/4242.sock' }),
    wakeRow({ id: 's2', label: 'docs2', messagingSocket: '/tmp/cc-socks/1.sock', messagingSocketConflict: '/tmp/cc-socks/9.sock' }),
    wakeRow({ id: 'f1', label: 'panel-mod', ownerSession: OTHER }),
  ]
  const { clock, state } = await boot($, on, rows)
  await clock.advance(5000)

  const list = (await $.tool.call({ tool: toolName(PLUGIN, 'rounds') })) as any
  const lines = String(list.result).split('\n')
  expect(lines).toHaveLength(3)
  expect(lines[0].startsWith(' ▶  docs-sweep')).toBe(true)
  expect(lines[0]).toContain('state=running')
  expect(lines[0]).toContain('log=/tmp/panel-fixtures/logs/w1.log')
  expect(lines[0]).toContain('trail=/tmp/panel-fixtures/trails/w1.jsonl')
  expect(lines[0].endsWith('inbox=yes')).toBe(true)
  expect(lines[2].startsWith('⇄▶  panel-mod')).toBe(true)
  // Re-pinned 2026-10-06 (voice round), from endsWith('inbox=no'): the tool
  // line carries the reason (inboxReason), and the delivered send says the
  // tokenless round may read the note as a peer's (PRE_TOKEN_NOTE). The
  // `sent to ` prefix is what keeps the result from being an error.
  expect(lines[2].endsWith('inbox=no (' + INBOX_OLDER_LAUNCH + ')')).toBe(true)

  const sent = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label: 'docs-sweep', text: 'correction: re-read the spec' })) as any
  expect(sent.result).toBe('sent to docs-sweep · ' + PRE_TOKEN_NOTE)
  expect(sent.isError).toBeFalsy()
  expect(state.sends).toHaveLength(1)
  expect(state.sends[0].to).toBe('uds:/tmp/cc-socks/4242.sock')

  const refused = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label: 'panel-mod', text: 'hi' })) as any
  expect(refused.result).toBe('refused: panel-mod is not one of your running rounds')
  expect(refused.isError).toBe(true)
  expect(state.sends).toHaveLength(1)

  const conflict = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label: 'docs2', text: 'which one' })) as any
  expect(conflict.result).toBe('refused: docs2 has two inbox sockets (shared hook settings)')
  expect(conflict.isError).toBe(true)

  const long = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label: 'docs-sweep', text: 'x'.repeat(4001) })) as any
  expect(long.result).toBe('refused: message too long (4001 > 4000)')
  expect(long.isError).toBe(true)
  expect(state.sends).toHaveLength(1)
})

// 28. An empty registry answers `no rounds` — never an empty result.
test('rounds tool with nothing visible says no rounds', async ($, on) => {
  const { clock } = await boot($, on, [])
  await clock.advance(5000)
  const res = (await $.tool.call({ tool: toolName(PLUGIN, 'rounds') })) as any
  expect(res.result).toBe('no rounds')
})

// 29. The system section states only what the mod makes true, changes only
// with the toggle, and stays under 900 characters.
test('the system section follows the wake toggle', async ($, on) => {
  const FACTS = {
    model: 'glm-5.3',
    promptModel: 'glm-5.3',
    surfaces: ['terminal'],
    tools: [],
    outputStyle: null,
    traits: [],
  }
  const { clock } = await boot($, on, [wakeRow()])
  await clock.advance(5000)
  const onSections = (await $.prompt.compose(FACTS)) as any
  const onSection = onSections.sections.find((s: any) => s.id === 'outsource-panel')
  expect(onSection?.scope).toBe('session')
  expect(onSection?.text).toBe(sectionText(true, PLUGIN))
  expect(sectionText(true, PLUGIN).length).toBeLessThanOrEqual(900)
  expect(sectionText(true, PLUGIN)).toContain('never approval')

  await $.command.run({ command: 'rounds', args: 'wake off' })
  const offSections = (await $.prompt.compose(FACTS)) as any
  const offSection = offSections.sections.find((s: any) => s.id === 'outsource-panel')
  expect(offSection?.text).toBe(sectionText(false, PLUGIN))
  expect(sectionText(false, PLUGIN).length).toBeLessThanOrEqual(900)
  expect(sectionText(false, PLUGIN)).toContain('wake is off')
})

// ---- defect fixes: the tick never waits on the wake; the known record ------

// 30. A submit that never settles must not stall the poll loop: the wake
// happens exactly while the lead works, and the pane, the band and the
// toasts are then at their most wanted. FAIL-first: with the submit awaited
// in the tick, runsCalls stops advancing and the second toast never fires.
test('wake: a submit that never settles does not stall the polls', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow(), wakeRow({ id: 'w2', label: 'second-fix' })])
  state.submitMode = 'never'
  await clock.advance(5000) // poll 1 seeds
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000) // poll 2: transition, submit #1 goes out and hangs
  expect(state.prompts).toHaveLength(1)
  const pollsBefore = state.runsCalls
  await clock.advance(5000)
  await clock.advance(5000)
  expect(state.runsCalls).toBeGreaterThan(pollsBefore) // ticks still run
  flipDone(state.rows.find((r) => r.id === 'w2') as any)
  await clock.advance(5000)
  expect(state.toasts).toContain('✅ second-fix done · 1h01m') // toasts still fire
  expect(state.prompts).toHaveLength(1) // buffered, not submitted while #1 hangs
  expect(state.store['woken']).toEqual({ [OWNER]: { w1: 'done', w2: 'done' } }) // recorded anyway
})

// 31. Coalescing: transitions found while a submit is outstanding leave as
// ONE submit when it settles. FAIL-first: without the single-outstanding
// guard, the second and third transitions are submitted at once (three
// submits, two outstanding).
test('wake: transitions found while a submit is outstanding coalesce into one', async ($, on) => {
  const rows = [
    wakeRow(),
    wakeRow({ id: 'w2', label: 'second-fix' }),
    wakeRow({ id: 'w3', label: 'third-fix' }),
  ]
  const { clock, state } = await boot($, on, rows)
  state.submitDefers = []
  await clock.advance(5000) // poll 1 seeds
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000) // poll 2: submit #1, held by the gate
  expect(state.prompts).toHaveLength(1)
  flipDone(state.rows.find((r) => r.id === 'w2') as any)
  await clock.advance(5000) // poll 3: buffered
  flipDone(state.rows.find((r) => r.id === 'w3') as any)
  await clock.advance(5000) // poll 4: buffered
  expect(state.prompts).toHaveLength(1)
  state.submitDefers[0].resolve({ text: state.prompts[0] })
  await clock.settle()
  expect(state.prompts).toHaveLength(2) // one more, not two
  expect(state.prompts[1]).toBe(
    wakeText(
      [
        { row: state.rows.find((r) => r.id === 'w2') as any, kind: 'done' },
        { row: state.rows.find((r) => r.id === 'w3') as any, kind: 'done' },
      ],
      BIN,
    ),
  )
})

// 32. A refused submit is not a delivered one: the transition is un-recorded
// and the next poll submits it again. FAIL-first: without the un-record, the
// store keeps the round as delivered and the second submit never happens.
test('wake: a rejected submit is retried on the next poll', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow()])
  state.submitFails = 1
  await clock.advance(5000)
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000) // poll 2: submit #1 rejects → un-recorded, queued
  expect(state.prompts).toHaveLength(1)
  await clock.advance(5000) // poll 3: the retry
  expect(state.prompts).toHaveLength(2)
  expect(state.prompts[1]).toBe(wakeText([{ row: state.rows.find((r) => r.id === 'w1') as any, kind: 'done' }], BIN))
  expect(state.store['woken']).toEqual({ [OWNER]: { w1: 'done' } })
})

// 33. An orphan that happened while the session was down catches up: orphan
// rows carry no finishedAt, so the anchor is startedAt. FAIL-first: with
// only finishedAt in the filter, the orphan is never a candidate.
test('wake: an orphan that happened while the session was down catches up', async ($, on) => {
  const row = wakeRow({
    id: 'orph1',
    label: 'night-orphan',
    state: 'orphan',
    pid: null,
    startedAt: NOW_MS / 1000 - 3600,
    finishedAt: null,
    elapsedSeconds: 3600,
    idleSeconds: null,
  })
  const { clock, state } = await boot($, on, [row], {
    store: { seen: { [OWNER]: NOW_MS - 2 * 3600 * 1000 } }, // the lead restarted while it ran
  })
  await clock.advance(5000)
  expect(state.prompts).toHaveLength(1)
  expect(state.prompts[0]).toContain('- night-orphan: orphan — pid gone')
})

// 34. A row already terminal at a session's first-ever poll is recorded as
// seen there, so a later resume does not replay it. FAIL-first: without the
// known-recording, the resume catch-up finds the orphan unseen and wakes.
test('wake: a row already orphan at the first-ever poll stays silent on a later resume', async ($, on) => {
  const row = wakeRow({
    id: 'orph1',
    label: 'night-orphan',
    state: 'orphan',
    pid: null,
    startedAt: NOW_MS / 1000 - 3600,
    finishedAt: null,
    elapsedSeconds: 3600,
    idleSeconds: null,
  })
  const { clock, state } = await boot($, on, [row])
  await clock.advance(5000) // first-ever poll: no watermark, seeds silently
  expect(state.prompts).toEqual([])
  expect(state.store['woken']).toEqual({ [OWNER]: { orph1: 'orphan' } })
  await reRegister($) // a later resume: catch-up re-armed against the store
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
})

// 35. A round that finished while wake was off was still SEEN: turning wake
// back on, or resuming, must not replay it. FAIL-first: with the recording
// skipped while wake is off, the resume catch-up finds it unseen and wakes.
test('wake: a round that finished while wake was off never wakes', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow()])
  await clock.advance(5000)
  await $.command.run({ command: 'rounds', args: 'wake off' })
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000) // toasted, recorded as seen, not submitted
  expect(state.toasts).toEqual(['✅ api-fix done · 1h01m'])
  expect(state.prompts).toEqual([])
  await $.command.run({ command: 'rounds', args: 'wake on' })
  await clock.advance(5000) // nothing re-derives it: it is known
  expect(state.prompts).toEqual([])
  await reRegister($) // nor on resume
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
})

// ---- the bundle: one module, two plugins ---------------------------------------
//
// Added 2026-10-06 (bundle round). The root plugin `outsource` carries this
// module (hooks/hooks.json at the repo root), so one marketplace install brings
// the skill and the panel; the standalone `outsource-panel` keeps working.

const COMPOSE_FACTS = {
  model: 'glm-5.3',
  promptModel: 'glm-5.3',
  surfaces: ['terminal'],
  tools: [],
  outputStyle: null,
  traits: [],
}

// 36. Binary discovery, the pure order: OUTSOURCE_PANEL_BIN, this plugin's own
// folder (the root plugin: marketplace cache or a clone), the clone two levels
// above a standalone copy, install.sh's place. FAIL-first: with the root and
// standalone candidates swapped, the cache layout's list comes back reordered.
test('binCandidates: the four layouts, most specific first', () => {
  const H = '/Users/exmpl'
  const tail = '/skills/outsource/bin/outsource'
  const home = H + '/.claude' + tail
  // marketplace install: the root plugin in the plugin cache
  const cache = H + '/.claude/plugins/cache/outsource/outsource/0.20.0'
  expect(binCandidates({ root: cache, home: H, envBin: undefined })).toEqual([
    cache + tail,
    H + '/.claude/plugins/cache/outsource' + tail,
    home,
  ])
  // --plugin-dir <clone>: the root plugin in a clone
  const clone = H + '/src/outsource'
  expect(binCandidates({ root: clone, home: H, envBin: '' })).toEqual([clone + tail, H + tail, home])
  // --plugin-dir <clone>/mods/outsource-panel: the standalone, its clone two up
  // (spelled without `..`; a trailing slash on the root changes nothing)
  const standalone = clone + '/mods/outsource-panel'
  expect(binCandidates({ root: standalone, home: H })).toEqual([standalone + tail, clone + tail, home])
  expect(binCandidates({ root: standalone + '/', home: H })[1]).toBe(clone + tail)
  // install.sh's copy is the last resort; the override beats everything
  expect(binCandidates({ root: standalone, home: H, envBin: '/opt/outsource' })).toEqual([
    '/opt/outsource',
    standalone + tail,
    clone + tail,
    home,
  ])
  // no HOME, no home candidate
  expect(binCandidates({ root: clone, home: undefined })).toEqual([clone + tail, H + tail])
})

// 37. register.js asks in binCandidates' order and the first that exists wins;
// the choice goes to the debug log once. Here only install.sh's copy exists.
// FAIL-first: with the old `HOME`-only lookup, existsAsked is the home path
// alone.
test('binary: the first existing candidate wins, asked in order', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows, { env: { HOME: '/fakehome' }, exists: (p) => p === BIN })
  const root = state.existsAsked[0].replace(/\/skills\/outsource\/bin\/outsource$/, '')
  expect(state.existsAsked).toEqual(binCandidates({ root, home: '/fakehome' }))
  // $.plugin.root is the folder holding this copy's plugin.json: the
  // standalone's sits in mods/outsource-panel, its second candidate is the
  // clone's binary.
  if (PLUGIN === PANEL_NAME) {
    expect(root.endsWith('/mods/outsource-panel')).toBe(true)
    expect(state.existsAsked[1]).toBe(root.slice(0, -'/mods/outsource-panel'.length) + '/skills/outsource/bin/outsource')
  } else {
    expect(root.endsWith('/mods/outsource-panel')).toBe(false)
  }
  expect(state.logs.filter((l) => l.includes('binary'))).toEqual([logText('binary: ' + BIN)])
  await clock.advance(5000)
  expect(state.runsCalls).toBe(1) // `runs json` went to the binary that won
})

// 38. Where this plugin's own folder holds the binary (the root plugin), that
// one wins and nothing past it is asked.
test('binary: this plugin\'s own folder beats install.sh', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows, { env: { HOME: '/fakehome' } }) // every path exists
  const own = state.existsAsked[0]
  expect(own.endsWith('/skills/outsource/bin/outsource')).toBe(true)
  expect(state.existsAsked).toEqual([own])
  state.bin = own
  await clock.advance(5000)
  expect(state.runsCalls).toBe(1)
  expect(state.logs).toContain(logText('binary: ' + own))
})

// 39. None found: every candidate is named, in the log and the pane, and
// nothing polls.
test('binary: none found names every candidate', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows, { exists: () => false })
  expect(state.existsAsked).toHaveLength(4) // the override, two under this plugin's root, install.sh's
  expect(state.existsAsked[0]).toBe(BIN)
  expect(state.logs).toContain(logText('binary not found; tried ' + state.existsAsked.join(', ')))
  await clock.advance(5000)
  expect(state.runsCalls).toBe(0)
  const ui = await mountPane($, 600)
  expect((await texts(ui))[0]).toBe('outsource binary not found: ' + state.existsAsked.join(' · '))
  await ui.unmount()
  const tool = (await $.tool.call({ tool: toolName(PLUGIN, 'rounds') })) as any
  expect(tool.result).toBe('outsource binary not found: ' + state.existsAsked.join(' · '))
  expect(tool.isError).toBe(true)
})

// 40. The section names this copy's tools. Literal names on purpose: this is
// the contract the model reads. FAIL-first: with the old hard-coded section,
// the bundled text names mcp__outsource-panel__….
test('sectionText names the tools of the copy it is given', () => {
  for (const wake of [true, false]) {
    const bundled = sectionText(wake, 'outsource')
    expect(bundled).toContain('mcp__outsource__rounds')
    expect(bundled).toContain('mcp__outsource__round_send')
    expect(bundled).not.toContain('mcp__outsource-panel__')
    const standalone = sectionText(wake, 'outsource-panel')
    expect(standalone).toContain('mcp__outsource-panel__rounds')
    expect(standalone).toContain('mcp__outsource-panel__round_send')
    expect(standalone).not.toContain('mcp__outsource__')
  }
  // A missing name is a programming error, not a section naming mcp__undefined__.
  expect(() => (sectionText as any)(true)).toThrow()
})

// 41. Names follow $.plugin.name in the running module: the registered tools,
// which tool calls it answers (its own; the other copy's pass on), the
// section. This test runs under both copies (see the header), so it is the
// `outsource` and the `outsource-panel` case in turn. FAIL-first: against the
// old literal matchers, the root run answers mcp__outsource-panel__rounds
// itself instead of passing it on, and its section names the wrong tools.
test('names follow $.plugin.name: registered tools, tool calls, section', async ($, on) => {
  on('tool.call', ($, e: any) => ({ result: 'passed on: ' + e.tool })) // beneath every plugin
  const { clock, state } = await boot($, on, [wakeRow()])
  expect([BUNDLED_NAME, PANEL_NAME]).toContain(PLUGIN)
  const other = PLUGIN === BUNDLED_NAME ? PANEL_NAME : BUNDLED_NAME
  expect(state.registered).toEqual(['mcp__' + PLUGIN + '__rounds', 'mcp__' + PLUGIN + '__round_send'])
  expect(state.logs).toContain(logText('tools: ' + state.registered.join(', ')))
  await clock.advance(5000)

  const own = (await $.tool.call({ tool: 'mcp__' + PLUGIN + '__rounds' })) as any
  expect(String(own.result).startsWith(' ▶  api-fix')).toBe(true)
  for (const tool of ['rounds', 'round_send']) {
    const theirs = (await $.tool.call({ tool: 'mcp__' + other + '__' + tool, label: 'api-fix', text: 'x' } as any)) as any
    expect(theirs.result).toBe('passed on: mcp__' + other + '__' + tool)
  }
  expect(state.sends).toHaveLength(0)

  const sections = (await $.prompt.compose(COMPOSE_FACTS)) as any
  const text = sections.sections.find((s: any) => s.id === 'outsource-panel')?.text ?? ''
  expect(text).toContain('mcp__' + PLUGIN + '__rounds')
  expect(text).toContain('mcp__' + PLUGIN + '__round_send')
  expect(text).not.toContain('mcp__' + other + '__')
})

// 42. Every log site the session path reaches goes to the debug log alone,
// prefixed once (checkLog). FAIL-first: with `{ to: 'debug' }` dropped from
// the log helper, every entry is a violation.
test('logs: every site goes to the debug log, prefixed once', async ($, on) => {
  const rows = [
    wakeRow({ messagingSocket: '/tmp/cc-socks/1.sock' }),
    wakeRow({ id: 'w2', label: 'second-fix' }),
    wakeRow({ id: 'w3', label: 'third-fix' }),
  ]
  const before = LOG_VIOLATIONS.length
  const { clock, state } = await boot($, on, rows) // binary, tools
  state.submitFails = 1
  await clock.advance(5000) // first poll: no watermark
  await $.command.run({ command: 'rounds', args: 'wake off' })
  await $.command.run({ command: 'rounds', args: 'wake on' })
  await $.command.run({ command: 'rounds', args: 'send api-fix hello' })
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000) // waking, then the refused submit
  await clock.advance(5000) // the retry
  state.submitDefers = []
  flipDone(state.rows.find((r) => r.id === 'w2') as any)
  await clock.advance(5000) // waking, held
  flipDone(state.rows.find((r) => r.id === 'w3') as any)
  await clock.advance(5000) // buffered
  const stems = [
    'binary: ',
    'tools: ',
    'no watermark for this session',
    'wake off',
    'wake on',
    '→ api-fix: hello',
    'waking the lead model',
    'wake submit failed for api-fix',
    'wake buffered while a submit is outstanding',
  ]
  for (const stem of stems) {
    expect(state.logs.some((l) => l.startsWith(logText(stem))), stem).toBe(true)
  }
  expect(state.logEntries.every((l) => l.to === 'debug')).toBe(true)
  expect(LOG_VIOLATIONS.slice(before)).toEqual([])
})

// A stand-in for the bundled copy: what `outsource` does at session.start
// that a standalone copy could collide with. An inline plugin closes over
// nothing of this file, so it cannot load the real module — this is a
// simulation of the other copy; the live proof is the M4 capture.
const BUNDLED_STANDIN = {
  name: 'outsource',
  register(on: any) {
    on('session.start', async ($: any, e: any, next: any) => {
      await $.command.register({ name: 'rounds', description: 'stand-in for the bundled panel' })
      await $.tool.register({ name: 'rounds', description: 'stand-in for the bundled panel' })
      return next(e)
    })
  },
}

// 43. Never two panels, the standalone-first half: an inline plugin of the
// user tier is admitted after the plugin under test, so the standalone judges
// the bundled copy's admission and stands down — no command, no tool, no
// binary lookup, no poll, no section, no pane, no band, and every call it
// would have answered passes on. The other half (the bundled copy admitted
// first refuses the standalone) needs the root plugin under test:
// tests/panel-bundle.test.ts. FAIL-first: without the stand-down, the
// standalone registers rounds/round_send beside the stand-in.
test('pair: a bundled copy admitted after the standalone makes it stand down', { plugins: [BUNDLED_STANDIN] }, async ($, on) => {
  on('command.run', () => ({ text: 'passed on' }))
  on('tool.call', () => ({ result: 'passed on' }))
  let booted: Awaited<ReturnType<typeof boot>>
  try {
    booted = await boot($, on, fixtureRows)
  } catch (err) {
    // The root run: the plugin under test is itself `outsource`, and the
    // harness loads no two plugins of one name. Nothing of the standalone
    // half can run there; the refusing half does (panel-bundle.test.ts).
    expect(String(err)).toContain('outsource is loaded twice')
    return
  }
  const { clock, state } = booted
  expect(state.commands).toEqual(['outsource:rounds']) // the stand-in's alone
  expect(state.registered).toEqual(['mcp__outsource__rounds'])
  const mine = state.logEntries.filter((l) => l.origin === PANEL_NAME)
  expect(mine).toHaveLength(1)
  expect(mine[0].to).toBe('debug')
  expect(mine[0].text.startsWith(logText('standing down — the bundled outsource plugin ('))).toBe(true)
  expect(state.existsAsked).toEqual([])
  await clock.advance(20000)
  expect(state.runsCalls).toBe(0)

  const run = await $.command.run({ command: 'rounds', args: '' })
  expect(run.text).toBe('passed on')
  expect(state.opens).toEqual([])
  const call = (await $.tool.call({ tool: 'mcp__outsource__rounds' })) as any
  expect(call.result).toBe('passed on')
  const sections = (await $.prompt.compose(COMPOSE_FACTS)) as any
  expect(sections.sections.some((s: any) => s.id === 'outsource-panel')).toBe(false)
  const pane = await $.ui.mount({ plugin: PANEL_NAME, surface: 'terminal', component: 'Pane', props: PANE_PROPS(120, 30), requestId: 'outsource-rounds' })
  expect(await texts(pane)).toEqual([])
  await pane.unmount()
  const band = await $.ui.mount({ plugin: PANEL_NAME, surface: 'terminal', component: 'AbovePrompt', props: BAND_PROPS(100) })
  expect(await texts(band)).toEqual([])
  await band.unmount()
})

// ---- the whole-panel switch: /rounds off|on -------------------------------------
//
// Added 2026-10-06 (bundle round, the person's decision): the panel is on by
// default and `/rounds off` turns ALL of it off, `/rounds on` back on, kept in
// the store ('enabled'). `/rounds wake off` stays the wake alone.

// 44. Off: not one process across several periods, no toast, no submit, no
// pane, no band; the section is the one off line; both tools answer the off
// line as an error; every command but on/off/wake answers it too.
// FAIL-first: with the timer left running after `/rounds off`, processCalls
// grows by one `runs json` per period and the done row toasts.
test('panel off: nothing polls, toasts, wakes or draws; the tools answer the off line', async ($, on) => {
  const { clock, state } = await boot($, on, [
    wakeRow(),
    wakeRow({ id: 'w2', label: 'second-fix', messagingSocket: '/tmp/cc-socks/2.sock' }),
  ])
  await clock.advance(5000) // poll 1 seeds
  const off = await $.command.run({ command: 'rounds', args: 'off' })
  expect(off.text).toBe(PANEL_OFF_TEXT)
  expect(state.store['enabled']).toBe(false)

  const calls = state.processCalls
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000)
  await clock.advance(5000)
  await clock.advance(5000)
  expect(state.processCalls).toBe(calls)
  expect(state.toasts).toEqual([])
  expect(state.prompts).toEqual([])

  expect((await $.command.run({ command: 'rounds', args: '' })).text).toBe(PANEL_OFF_TEXT)
  expect((await $.command.run({ command: 'rounds', args: 'api-fix' })).text).toBe(PANEL_OFF_TEXT)
  expect((await $.command.run({ command: 'rounds', args: 'send second-fix hi' })).text).toBe(PANEL_OFF_TEXT)
  expect(state.opens).toEqual([])
  expect(state.sends).toEqual([])
  const band = await mountBand($, 100)
  expect(await texts(band)).toEqual([])
  await band.unmount()
  const pane = await mountPane($, 120)
  expect(await texts(pane)).toEqual([])
  await pane.unmount()

  const sections = (await $.prompt.compose(COMPOSE_FACTS)) as any
  const section = sections.sections.filter((s: any) => s.id === 'outsource-panel')
  expect(section).toHaveLength(1)
  expect(section[0].text).toBe(offSectionText(PLUGIN))
  expect(section[0].text).toContain('no wake will come')
  for (const tool of ['rounds', 'round_send']) {
    const res = (await $.tool.call({ tool: toolName(PLUGIN, tool), label: 'second-fix', text: 'x' } as any)) as any
    expect(res.result).toBe(PANEL_OFF_TEXT)
    expect(res.isError).toBe(true)
  }
  expect(state.sends).toEqual([])
  expect(state.processCalls).toBe(calls)
})

// 45. Back on: polling resumes, and the first poll seeds without waking for
// what changed while off (seeded reset → baseline only; markKnown records the
// row) — while a change after it toasts and wakes as ever, and a later resume
// does not replay the off-time change. FAIL-first: without the seeded reset,
// the first poll back toasts and wakes for api-fix, which finished while off.
test('panel on: polling resumes, nothing that changed while off wakes', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow(), wakeRow({ id: 'w2', label: 'second-fix' })])
  await clock.advance(5000) // poll 1 seeds
  await $.command.run({ command: 'rounds', args: 'off' })
  flipDone(state.rows.find((r) => r.id === 'w1') as any) // finishes while off
  await clock.advance(10000)

  const back = await $.command.run({ command: 'rounds', args: 'on' })
  expect(back.text).toBe(PANEL_ON_TEXT)
  expect(state.store['enabled']).toBe(true)
  const before = state.runsCalls
  await clock.advance(5000) // the first poll back
  expect(state.runsCalls).toBe(before + 1)
  expect(state.toasts).toEqual([])
  expect(state.prompts).toEqual([])
  expect((state.store['woken'] as any)[OWNER].w1).toBe('done') // known, never woken

  flipDone(state.rows.find((r) => r.id === 'w2') as any) // finishes while on
  await clock.advance(5000)
  expect(state.toasts).toEqual(['✅ second-fix done · 1h01m'])
  expect(state.prompts).toHaveLength(1)
  expect(state.prompts[0]).toContain('second-fix')
  expect(state.prompts[0]).not.toContain('api-fix')

  await reRegister($) // a resume re-arms the catch-up against the store
  await clock.advance(5000)
  expect(state.prompts).toHaveLength(1)
})

// 46. The rule when the panel was off from the start: a resumed session with
// a watermark and an own round that finished unseen would wake through the
// resume catch-up — but turning the panel on disarms it, so that round is
// taken as baseline too. FAIL-first: without `catchupArmed = false` in the
// switch, the first poll back wakes for late-fix.
test('panel on after starting off: the resume catch-up is disarmed too', async ($, on) => {
  const row = wakeRow({ id: 'late1', label: 'late-fix' })
  flipDone(row as any, 3600) // finished 1 h ago
  const { clock, state } = await boot($, on, [row], {
    store: { enabled: false, seen: { [OWNER]: NOW_MS - 2 * 3600 * 1000 } },
  })
  await clock.advance(10000)
  expect(state.processCalls).toBe(0) // off from the store: no poll at all
  expect(state.logs).toContain(logText('the panel is off (store) — /rounds on turns it on'))
  await $.command.run({ command: 'rounds', args: 'on' })
  await clock.advance(5000)
  expect(state.runsCalls).toBe(1)
  expect(state.prompts).toEqual([])
  expect(state.toasts).toEqual([])
  expect(state.store['woken']).toEqual({ [OWNER]: { late1: 'done' } })
})

// 47. The switch lives in the store: a re-register (hot reload, resume) keeps
// it off. `on`/`off` are subcommand words before labels, take no argument, and
// `/rounds wake` stays independent of them. FAIL-first: with session.start not
// reading 'enabled', the re-register turns polling back on.
test('panel off survives a re-register; on/off are subcommand words; wake is its own switch', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow(), wakeRow({ id: 'loff', label: 'off' })])
  await clock.advance(5000)
  // A round labelled `off` does not take the word: the panel goes off.
  expect((await $.command.run({ command: 'rounds', args: 'off' })).text).toBe(PANEL_OFF_TEXT)
  expect(state.opens).toEqual([])
  expect((await $.command.run({ command: 'rounds', args: 'on now' })).text).toBe('usage: /rounds on | /rounds off')

  await reRegister($)
  const calls = state.processCalls
  await clock.advance(15000)
  expect(state.processCalls).toBe(calls)
  expect((await $.command.run({ command: 'rounds', args: '' })).text).toBe(PANEL_OFF_TEXT)
  const sections = (await $.prompt.compose(COMPOSE_FACTS)) as any
  expect(sections.sections.find((s: any) => s.id === 'outsource-panel')?.text).toBe(offSectionText(PLUGIN))

  // The wake switch still answers while the panel is off, and changes only
  // the wake; the panel stays off.
  const wake = await $.command.run({ command: 'rounds', args: 'wake off' })
  expect(wake.text).toBe('wake is off — toasts only (the panel itself is off — /rounds on turns it on)')
  expect(state.store['wake']).toBe(false)
  expect(state.store['enabled']).toBe(false)
  await clock.advance(10000)
  expect(state.processCalls).toBe(calls)
  // Saying the state it is already in changes nothing.
  expect((await $.command.run({ command: 'rounds', args: 'off' })).text).toBe(PANEL_OFF_TEXT)
})

// 48. Off while the pane is open closes it, and the close starts no timer.
// FAIL-first: without the close in the switch, ui.close is never asked and the
// pane stays up drawing a panel that is off.
test('panel off while the pane is open closes it', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  await $.command.run({ command: 'rounds', args: '' }) // open
  await clock.advance(2000)
  expect(state.opens).toHaveLength(1)
  await $.command.run({ command: 'rounds', args: 'off' })
  expect(state.closes.map((c) => c.id)).toEqual(['outsource-rounds'])
  const calls = state.processCalls
  await clock.advance(10000)
  expect(state.processCalls).toBe(calls)
  // Back on, a bare /rounds opens it again.
  await $.command.run({ command: 'rounds', args: 'on' })
  await $.command.run({ command: 'rounds', args: '' })
  expect(state.opens).toHaveLength(2)
})

// 49. A wake buffered behind an outstanding submit never leaves once the
// panel is off: off means no wake submits, including the ones it held.
// FAIL-first: without the switch emptying the buffer, the buffer leaves as a
// second submit after the panel went off.
test('panel off: a buffered wake never leaves', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow(), wakeRow({ id: 'w2', label: 'second-fix' })])
  state.submitDefers = []
  await clock.advance(5000) // poll 1 seeds
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000) // submit #1, held
  flipDone(state.rows.find((r) => r.id === 'w2') as any)
  await clock.advance(5000) // buffered
  expect(state.prompts).toHaveLength(1)
  await $.command.run({ command: 'rounds', args: 'off' })
  state.submitDefers[0].resolve({ text: state.prompts[0] })
  await clock.settle()
  expect(state.prompts).toHaveLength(1)
})

// 50. A refused submit queued for retry is dropped by `/rounds off`, so the
// first poll back on does not deliver it: it changed before the panel went
// off and was never delivered, and back on seeds without waking. FAIL-first:
// without the switch emptying the retry queue, the first poll back resubmits.
test('panel off: a queued retry does not leave when the panel comes back', async ($, on) => {
  const { clock, state } = await boot($, on, [wakeRow()])
  state.submitFails = 1
  await clock.advance(5000) // poll 1 seeds
  flipDone(state.rows.find((r) => r.id === 'w1') as any)
  await clock.advance(5000) // submit #1 refused: un-recorded, queued for retry
  expect(state.prompts).toHaveLength(1)
  await $.command.run({ command: 'rounds', args: 'off' })
  await $.command.run({ command: 'rounds', args: 'on' })
  await clock.advance(5000) // the first poll back
  await clock.advance(5000)
  expect(state.prompts).toHaveLength(1)
  expect(state.store['woken']).toEqual({ [OWNER]: { w1: 'done' } }) // known from the poll back
})

// ---- the lead's voice -------------------------------------------------------
//
// A round's prompt opens with a launcher notice naming its lead two ways: the
// socket it was launched from, and a per-launch token. A message whose first
// line is `lead-token: <token>` is an amendment with the spec's authority;
// anything else from another session is information only. Rows are built
// inline (the fixture is pinned by the capture checks and carries no token).

const TOKEN = '00112233445566778899aabbccddeeff'

// The token goes out as the note's first line; the log and the toast carry
// the person's text only, and the 4000 cap is on that text. FAIL-first: with
// register.js sending `text` instead of leadMessage(target, text),
// sends[0].text reads 'drop the 9-minute cap'.
test('round_send prefixes the lead token', async ($, on) => {
  const { clock, state } = await boot($, on, [
    wakeRow({ id: 't1', label: 'tok-round', messagingSocket: '/tmp/cc-socks/7001.sock', leadToken: TOKEN } as any),
  ])
  await clock.advance(5000)
  const res = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label: 'tok-round', text: 'drop the 9-minute cap' })) as any
  expect(res.result).toBe('sent to tok-round')
  expect(res.isError).toBeFalsy()
  expect(state.sends).toHaveLength(1)
  expect(state.sends[0].to).toBe('uds:/tmp/cc-socks/7001.sock')
  expect(state.sends[0].text).toBe('lead-token: ' + TOKEN + '\ndrop the 9-minute cap')
  expect(state.toasts).toEqual(['sent to tok-round'])
  expect(state.logs.some((l) => l.includes(TOKEN))).toBe(false)

  const atCap = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label: 'tok-round', text: 'x'.repeat(4000) })) as any
  expect(atCap.result).toBe('sent to tok-round') // the cap counts the person's text, not the prefix
  expect(state.sends[1].text).toBe('lead-token: ' + TOKEN + '\n' + 'x'.repeat(4000))
})

// A row with no token (launched before lead tokens) sends exactly as before,
// and a delivered send says the round may read it as a peer's; a send that
// was not delivered says only that. FAIL-first: without the PRE_TOKEN_NOTE
// branch the result reads 'sent to old-round'.
test("round_send to an older launch says the note may read as a peer's", async ($, on) => {
  const { clock, state } = await boot($, on, [
    wakeRow({ id: 'o1', label: 'old-round', messagingSocket: '/tmp/cc-socks/7002.sock' }),
  ])
  await clock.advance(5000)
  const res = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label: 'old-round', text: 'fix the path' })) as any
  expect(res.result).toBe(
    "sent to old-round · this round was launched before lead tokens — it may treat the note as a peer's; relaunch with --session for a binding correction",
  )
  expect(res.isError).toBeFalsy()
  expect(state.sends[0].text).toBe('fix the path')

  state.sendResult = { isDelivered: false, reason: 'nobody home' }
  const failed = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label: 'old-round', text: 'again' })) as any
  expect(failed.result).toBe('not delivered to old-round: nobody home')
})

// The four reasons a row cannot take a message, their order, and the three
// places they show: the round_send refusal, the `rounds` tool line, the
// pane's line under the selected round. FAIL-first: with findTarget back to
// its bare 'has no inbox', the refusal lines read 'refused: crush-round has
// no inbox'.
test('inbox reasons: the four cases and where they show', async ($, on) => {
  const crush = wakeRow({ id: 'i1', label: 'crush-round', harness: 'crush' })
  const done = wakeRow({ id: 'i2', label: 'done-round', state: 'done', rc: 0, pid: null, finishedAt: NOW_MS / 1000 - 10, idleSeconds: null, leadToken: 'ab' } as any)
  const older = wakeRow({ id: 'i3', label: 'older-round' })
  const pending = wakeRow({ id: 'i4', label: 'pending-round', leadToken: 'cd' } as any)
  const live = wakeRow({ id: 'i5', label: 'live-round', leadToken: 'ef', messagingSocket: '/tmp/cc-socks/7003.sock' } as any)
  expect(inboxReason(crush)).toBe('harness crush has no inbox')
  expect(inboxReason(done)).toBe('round not running')
  expect(inboxReason(older)).toBe('launched before inbox support — cannot receive; stop it and resume with --session')
  expect(INBOX_OLDER_LAUNCH).toBe(inboxReason(older))
  expect(inboxReason(pending)).toBe('inbox not revealed yet')
  expect(inboxReason(live)).toBe(null)
  expect(inboxReason({ ...crush, state: 'done' })).toBe('harness crush has no inbox') // the harness comes first

  const { clock, state } = await boot($, on, [crush, done, older, pending, live])
  await clock.advance(5000)
  for (const [label, reason] of [
    ['crush-round', 'harness crush has no inbox'],
    ['older-round', INBOX_OLDER_LAUNCH],
    ['pending-round', 'inbox not revealed yet'],
  ]) {
    const res = (await $.tool.call({ tool: toolName(PLUGIN, 'round_send'), label, text: 'hi' })) as any
    expect(res.result).toBe('refused: ' + label + ': ' + reason)
    expect(res.isError).toBe(true)
  }
  expect(state.sends).toHaveLength(0)

  const list = String(((await $.tool.call({ tool: toolName(PLUGIN, 'rounds') })) as any).result).split('\n')
  const lineOf = (label: string) => list.find((l) => l.includes(label)) ?? ''
  expect(lineOf('crush-round').endsWith('inbox=no (harness crush has no inbox)')).toBe(true)
  expect(lineOf('done-round').endsWith('inbox=no (round not running)')).toBe(true)
  expect(lineOf('older-round').endsWith('inbox=no (' + INBOX_OLDER_LAUNCH + ')')).toBe(true)
  expect(lineOf('pending-round').endsWith('inbox=no (inbox not revealed yet)')).toBe(true)
  expect(lineOf('live-round').endsWith('inbox=yes')).toBe(true)

  await $.command.run({ command: 'rounds', args: '' })
  const ui = await mountPane($, 120)
  await ui.select({ key: 'round', value: 'i4' })
  await clock.settle()
  expect(await ui.find({ text: 'no inbox: inbox not revealed yet' })).toBeDefined()
  await ui.select({ key: 'round', value: 'i5' })
  await clock.settle()
  expect(await ui.find({ type: 'Input', key: 'msg' })).toBeDefined()
  await ui.unmount()
})

// ---- how a finished round ended ---------------------------------------------
//
// `runs json` carries what the wrapper learned (harnessSignal, signalSource)
// and what `runs stop` recorded (stopRequested). A stop the lead made itself
// is not news: ■, no wake. A signal nobody on record sent is: ✗, and the wake
// says "killed by an external TERM" rather than "failed rc=143".

const ended = (row: any, extra: Record<string, unknown> = {}, atSec = 10) =>
  Object.assign(row, {
    state: 'failed',
    rc: 143,
    pid: null,
    finishedAt: NOW_MS / 1000 - atSec,
    elapsedSeconds: 3665,
    idleSeconds: null,
    ...extra,
  })
const LEAD_STOP = { stopRequested: '2026-10-06T08:00:00Z', stopBy: OWNER, stopReason: 'wrong premise', harnessSignal: 'TERM', signalSource: 'lead-stop' }
const EXTERNAL = { harnessSignal: 'TERM', signalSource: 'external' }

// FAIL-first: with glyphFor's ■ branch removed, the stopped row draws ❌.
test('endings: a lead stop draws ■, an external kill ✗, any other failure ❌', () => {
  const stopped = ended(wakeRow({ id: 'e1', label: 'halted' }), LEAD_STOP)
  const shot = ended(wakeRow({ id: 'e2', label: 'shot' }), EXTERNAL)
  const plain = ended(wakeRow({ id: 'e3', label: 'plain' }))
  expect(endingOf(stopped)).toBe('stopped')
  expect(endingOf(shot)).toBe('external')
  expect(endingOf(plain)).toBe(null)
  expect(endingOf(wakeRow({ ...EXTERNAL } as any))).toBe(null) // still running: no ending yet
  const lines = [stopped, shot, plain].map((r) => rowLine(r, OWNER, 120))
  expect(lines[0].startsWith(' ■  halted')).toBe(true)
  expect(lines[0].endsWith(' stopped')).toBe(true)
  expect(lines[1].startsWith(' ✗  shot')).toBe(true)
  expect(lines[1].endsWith(' TERM ext')).toBe(true)
  expect(lines[2].startsWith(' ❌ plain')).toBe(true)
  expect(lines[2].endsWith(' rc=143')).toBe(true)
})

// The lead's own stop: a toast for the person, nothing for the model, and the
// round recorded as seen. An external TERM in the same session: one wake,
// worded as a kill, with the review block. FAIL-first: without deliverWake's
// isLeadStop filter the stop submits a prompt; without wakeLine's external
// branch the kill reads '- web-polish: failed rc=143 · …'.
test("wake: the lead's own stop wakes nobody; an external TERM wakes as a kill", async ($, on) => {
  const { clock, state } = await boot($, on, [
    wakeRow({ id: 'w1', label: 'api-fix' }),
    wakeRow({ id: 'w2', label: 'web-polish', log: '/tmp/panel-fixtures/logs/w2.log' }),
  ])
  await clock.advance(5000) // seed
  ended(state.rows.find((r) => r.id === 'w1') as any, LEAD_STOP)
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
  expect(state.toasts).toEqual(['■ api-fix stopped'])
  expect(state.store['woken']).toEqual({ [OWNER]: { w1: 'failed' } })

  ended(state.rows.find((r) => r.id === 'w2') as any, EXTERNAL)
  await clock.advance(5000)
  expect(state.prompts).toHaveLength(1)
  const lines = state.prompts[0].split('\n')
  expect(lines[0]).toContain('1 of your rounds changed state')
  expect(lines[1]).toBe('- web-polish: killed by an external TERM · 1h01m · log=/tmp/panel-fixtures/logs/w2.log')
  expect(lines[2]).toBe('Review web-polish:')
  expect(state.toasts).toEqual(['■ api-fix stopped', '✗ web-polish killed by an external TERM'])
})

// A lead stop that ended while the session was down stays silent at the
// resume catch-up too, and is recorded as seen. FAIL-first: without the
// isLeadStop filter the catch-up submits one prompt.
test("wake: a lead stop found at the resume catch-up stays silent", async ($, on) => {
  const row = ended(wakeRow({ id: 'late2', label: 'late-stop' }), LEAD_STOP, 3600)
  const { clock, state } = await boot($, on, [row], {
    store: { seen: { [OWNER]: NOW_MS - 2 * 3600 * 1000 } },
  })
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
  expect(state.store['woken']).toEqual({ [OWNER]: { late2: 'failed' } })
})

// A foreign round whose inbox works is not missing one: the line under it
// says whose it is. FAIL-first: with the pane's foreign branch removed, the
// line reads 'no inbox for this round'.
test('pane: a foreign round with an inbox says not your round', async ($, on) => {
  const { clock } = await boot($, on, [
    wakeRow({ id: 'g1', label: 'mine' }),
    wakeRow({ id: 'g2', label: 'theirs', ownerSession: OTHER, messagingSocket: '/tmp/cc-socks/7004.sock', leadToken: 'ab' } as any),
  ])
  await $.command.run({ command: 'rounds', args: '' })
  await clock.advance(2000)
  const ui = await mountPane($, 120)
  await ui.select({ key: 'round', value: 'g2' })
  await clock.settle()
  expect(await ui.find({ type: 'Input', key: 'msg' })).toBeUndefined()
  expect(await ui.find({ text: 'not your round' })).toBeDefined()
  expect(await ui.find({ text: 'no inbox for this round' })).toBeUndefined()
  await ui.unmount()
})

// ---- the plan-limit wait (track quota's `waiting` state) -------------------
//
// Track quota adds the registry state `waiting` (a round its plan limit cut,
// held by --resume-on-reset until the reset) and the fields quotaExhausted,
// resetAt and waitingUntil (RFC3339). This tree has no such state yet, so the
// rows are built inline. Times are built in the local zone, so hh:mm is the
// same on any machine: the panel shows a person their own clock.

const localISO = (h: number, m: number) => new Date(2026, 9, 6, h, m).toISOString()
const WAIT = { state: 'waiting', waitingUntil: localISO(21, 5), quotaExhausted: true, resetAt: localISO(21, 5) }
const CUT = { state: 'failed', rc: 1, pid: null, finishedAt: NOW_MS / 1000 - 10, elapsedSeconds: 3665, idleSeconds: null, quotaExhausted: true, resetAt: localISO(21, 5) }

// FAIL-first: with glyphFor's waiting branch removed, the waiting row draws
// a blank glyph and the ⏸ check fails.
test('quota: a waiting row draws ⏸ with its reset, a cut row ⛔, a resumed success ✅', () => {
  const waiting = wakeRow({ id: 'q1', label: 'held', ...WAIT } as any)
  const cut = wakeRow({ id: 'q2', label: 'cut', ...CUT } as any)
  const resumed = wakeRow({ id: 'q3', label: 'resumed', ...CUT, state: 'done', rc: 0 } as any)
  expect(isLive(waiting)).toBe(true)
  expect(endingOf(cut)).toBe('quota')
  expect(endingOf(resumed)).toBe(null)
  const [w, c, r] = [waiting, cut, resumed].map((row) => rowLine(row, OWNER, 120))
  expect(w.startsWith(' ⏸  held')).toBe(true)
  expect(w.endsWith(' quota → 21:05')).toBe(true)
  expect(c.startsWith(' ⛔ cut')).toBe(true)
  expect(c.endsWith(' resets 21:05')).toBe(true)
  expect(r.startsWith(' ✅ resumed')).toBe(true)
  expect(localHHMM(undefined)).toBe('?')
  expect(localHHMM('not a time')).toBe('?')
})

// A waiting row is live: listed with the running ones, given an activity
// line, eligible for the band. FAIL-first: with visibleRows back to
// state === 'running', the waiting row is not listed at all.
test('quota: a waiting row is live for the list, the activity lines and the band', () => {
  const waiting = wakeRow({ id: 'q1', label: 'held', ...WAIT } as any)
  const foreign = wakeRow({ id: 'q4', label: 'theirs', ownerSession: OTHER, ...WAIT } as any)
  const visible = visibleRows([waiting, foreign], OWNER, NOW_MS)
  expect(visible.map((r: any) => r.id)).toEqual(['q1', 'q4'])
  expect(activityRows(visible, OWNER).map((r: any) => r.id)).toEqual(['q1'])
  expect(bandRow([waiting, foreign], OWNER)?.id).toBe('q1')
})

// running→waiting is no news (the round resumes itself); waiting→failed by
// the plan limit wakes as a cut with its reset, waiting→done as done.
// FAIL-first: with transitionFor's live test back to prev.state ===
// 'running', the waiting→… transitions never wake; with wakeLine's quota
// branch removed, the cut reads '- api-fix: failed rc=1 · …'.
test('quota: running→waiting is silent, waiting→cut and waiting→done wake', async ($, on) => {
  const { clock, state } = await boot($, on, [
    wakeRow({ id: 'w1', label: 'api-fix' }),
    wakeRow({ id: 'w2', label: 'web-polish', log: '/tmp/panel-fixtures/logs/w2.log' }),
  ])
  await clock.advance(5000) // seed
  for (const id of ['w1', 'w2']) Object.assign(state.rows.find((r) => r.id === id) as any, WAIT)
  await clock.advance(5000)
  expect(state.prompts).toEqual([])
  expect(state.toasts).toEqual([])
  const listed = String(((await $.tool.call({ tool: toolName(PLUGIN, 'rounds') })) as any).result).split('\n')
  expect(listed[0].startsWith(' ⏸  api-fix')).toBe(true)
  expect(listed[0]).toContain('quota → 21:05')

  Object.assign(state.rows.find((r) => r.id === 'w1') as any, CUT)
  flipDone(state.rows.find((r) => r.id === 'w2') as any)
  await clock.advance(5000)
  expect(state.prompts).toHaveLength(1)
  const lines = state.prompts[0].split('\n')
  expect(lines[0]).toContain('2 of your rounds changed state')
  expect(lines[1]).toBe('- api-fix: cut by the plan limit (429), resets 21:05 · 1h01m · log=/tmp/panel-fixtures/logs/w1.log')
  expect(lines[2]).toBe('- web-polish: done rc=0 · 1h01m · log=/tmp/panel-fixtures/logs/w2.log')
  expect(state.toasts).toEqual(['⛔ api-fix cut by the plan limit (429), resets 21:05', '✅ web-polish done · 1h01m'])
})

// ---- the setup pane: /rounds setup ---------------------------------------------
//
// Added 2026-10-09 (setup-pane round). A second pane over the user's own
// choices — enabled and default model per provider — read and written only
// through the binary's `config` verbs (internal/config/cli.go), the one owner
// of the file and its rules. The stubs answer as that CLI does
// (configAnswer, modelsAnswer); the data is the committed fixtures.

const SETUP_PROPS = (bodyColumns: number, bodyRows: number) => ({
  title: 'outsource setup',
  isFocused: false,
  bodyColumns,
  placement: 'dock' as const,
  scroll: { offset: 0, bodyRows },
  view: {},
})

const mountSetup = ($: any, bodyColumns: number, bodyRows = 30) =>
  $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'Pane', props: SETUP_PROPS(bodyColumns, bodyRows), requestId: 'outsource-setup' })

const CONFIG_PATH = '/tmp/panel-fixtures/config.json'
const writesOf = (state: any): string[][] => state.argvs.filter((a: string[]) => a[1] === 'config' && (a[2] === 'set' || a[2] === 'unset'))
const listsOf = (state: any): number => state.argvs.filter((a: string[]) => a[1] === 'config' && a[2] === 'list').length
const modelsOf = (state: any): string[][] => state.argvs.filter((a: string[]) => a[1] === 'models')

// The setup pane's lines as drawn, top to bottom: a Text is its line, a row
// Box of Texts (a provider line) its Texts joined; the controls are skipped.
async function setupLines(ui: any): Promise<string[]> {
  const root = await ui.drawn()
  const textOf = (n: any): string => (n.children ?? []).filter((c: any) => typeof c === 'string').join('')
  const out: string[] = []
  for (const child of root.children ?? []) {
    if (child === null || typeof child !== 'object') continue
    const kids = (child.children ?? []).filter((c: any) => c !== null && typeof c === 'object')
    if (child.type === 'Text') out.push(textOf(child))
    else if (child.type === 'Box' && kids.length > 0 && kids.every((c: any) => c.type === 'Text')) out.push(kids.map(textOf).join(''))
  }
  return out
}

// The pure rules (view.js): the provider set is the binary's — every
// `providers.<p>.enabled` key, in key order — and nothing else. FAIL-first:
// with providerStates keeping only six hard-coded names, the `later` case
// loses `acme`; with the key test loosened to /^providers\.(.+)\.enabled$/,
// it lists a provider named `a.b`.
test('setup pure: the providers are the enabled keys, in key order; other key shapes are skipped', () => {
  const states = providerStates(CONFIG_LIST.values)
  expect(states.map((p: any) => p.name)).toEqual(['agy', 'muse', 'openrouter', 'xai', 'zai', 'zen'])
  expect(states.find((p: any) => p.name === 'muse')).toEqual({ name: 'muse', enabled: false, model: null })
  expect(states.find((p: any) => p.name === 'openrouter')).toEqual({
    name: 'openrouter',
    enabled: true,
    model: 'nvidia/nemotron-3-ultra-550b-a55b:free',
  })
  expect(states.find((p: any) => p.name === 'agy')).toEqual({ name: 'agy', enabled: true, model: null }) // null enabled: on
  const later = {
    'context.autoCompactWindow': 120000, // a parallel round's key: not the pane's
    'free.allowTraining': true,
    'providers.acme.enabled': null, // a provider no list here names
    'providers.acme.defaultModel': 'a-1',
    'providers.acme.contextWindow': 5, // a field the pane does not know
    'providers.enabled': false,
    'providers.a.b.enabled': false,
    'providers.zen.enabled': true,
  }
  expect(providerStates(later)).toEqual([
    { name: 'acme', enabled: true, model: 'a-1' },
    { name: 'zen', enabled: true, model: null },
  ])
  expect(providerStates({})).toEqual([])
  expect(parseConfigList(JSON.stringify(CONFIG_LIST))).toEqual({ path: CONFIG_PATH, values: CONFIG_LIST.values })
  expect(parseConfigList('{"path":1,"values":{}}')).toBe(null)
  expect(parseConfigList('not json')).toBe(null)
  expect(firstLine('\n  \nfirst\nsecond')).toBe('first')
  expect(firstLine('')).toBe(null)
  // An engine rejection loses only its own plugin's lead.
  expect(rejectionText(new Error('outsource-panel: $.process.run: timed out'), 'outsource-panel')).toBe('$.process.run: timed out')
  expect(rejectionText(new Error('outsource: $.process.run: timed out'), 'outsource-panel')).toBe('outsource: $.process.run: timed out')
  expect(rejectionText('plain', 'outsource')).toBe('plain')
})

// `<sel> <name10> <on|off3>  <model>`, cut to the width, as two segments so
// the model can be dim alone. FAIL-first: with the selected marker dropped,
// the openrouter line reads '  openrouter …'; with truncate removed, the
// 30-column line is 56 columns wide.
test('setup pure: the provider line — marker, padding, on/off, launcher default, a 30-column cut', () => {
  const or = { name: 'openrouter', enabled: true, model: 'nvidia/nemotron-3-ultra-550b-a55b:free' }
  const wide = providerLine(or, true, 120)
  expect(wide.text).toBe('› openrouter on   nvidia/nemotron-3-ultra-550b-a55b:free')
  expect([wide.head, wide.model, wide.headDim, wide.modelDim]).toEqual(['› openrouter on   ', 'nvidia/nemotron-3-ultra-550b-a55b:free', false, false])
  const agy = providerLine({ name: 'agy', enabled: true, model: null }, false, 120)
  expect(agy.text).toBe('  agy        on   launcher default')
  expect([agy.headDim, agy.modelDim]).toEqual([false, true]) // only the launcher default is dim
  const muse = providerLine({ name: 'muse', enabled: false, model: null }, false, 120)
  expect(muse.text).toBe('  muse       off  launcher default')
  expect([muse.headDim, muse.modelDim]).toEqual([true, true]) // a disabled provider: the whole line
  const narrow = providerLine(or, true, 30)
  expect(narrow.text).toBe('› openrouter on   nvidia/nemo…')
  expect(displayWidth(narrow.text)).toBe(30)
  expect(narrow.head + narrow.model).toBe(narrow.text)
  const tiny = providerLine(or, true, 10) // narrower than the head
  expect([tiny.text, tiny.model]).toEqual(['› openrou…', ''])
  expect(providerLine({ name: 'averyveryverylongname', enabled: true, model: 'm' }, false, 80).text).toBe('  averyvery… on   m')
})

// The free-model options. FAIL-first: without the router filter the router
// row is the first option and k-small falls off the 8.
test('setup pure: free model options — routers and deprecated dropped, context order, 8, labels', () => {
  const m = (id: string, over: Record<string, unknown> = {}) => ({
    provider: 'zen', id, name: id, context: 100000, free: true, tools: true, router: false, status: 'active', policy: 'trains', ...over,
  })
  const models = [
    m('k-small', { context: 8000 }),
    m('router-auto', { context: 9000000, router: true }),
    m('old-big', { context: 5000000, status: 'deprecated' }),
    m('beta-mid', { context: 64000, status: 'beta' }),
    m('b-tie', { context: 131072 }),
    m('a-tie', { context: 131072, policy: null }),
    m('null-status', { context: 200000, status: null }),
    m('no-ctx', { context: null, policy: 'retains' }),
    m('other', { provider: 'openrouter', context: 7000000 }),
    m('c', { context: 32768 }),
    m('d', { context: 16384 }),
    m('e', { context: 12000 }),
    m('f', { context: 10000 }),
    m('a-tie', { context: 131072 }), // the same id again: a Select's values are unique
  ]
  const options = freeModelOptions(models, 'zen')
  expect(options.map((o: any) => o.value)).toEqual(['null-status', 'a-tie', 'b-tie', 'c', 'd', 'e', 'f', 'k-small'])
  expect(options).toHaveLength(8) // no-ctx (the 9th) is cut
  expect(options[0].label).toBe('null-status · 200k · trains')
  expect(options[1].label).toBe('a-tie · 131k · unknown') // a null policy reads unknown
  expect(options[7].label).toBe('k-small · 8k · trains')
  const few = freeModelOptions([m('no-ctx', { context: null, policy: undefined }), m('one', { context: 1500 })], 'zen')
  expect(few.map((o: any) => o.label)).toEqual(['one · 2k · trains', 'no-ctx · ?k · unknown'])
  expect(freeModelOptions([], 'zen')).toEqual([])
  // The fixture, as the binary prints it: the deprecated row is dropped.
  const fromFixture = freeModelOptions(MODELS_ZEN.models, 'zen')
  expect(fromFixture.map((o: any) => o.label)).toEqual([
    'nemotron-3-ultra-free · 1000k · trains',
    'fixture-a-free · 262k · no-train-no-retain',
    'fixture-b-free · 200k · retains',
    'fixture-c1-free · 131k · unknown',
    'fixture-c2-free · 131k · unknown',
    'fixture-d-free · 128k · trains',
    'fixture-e-free · 66k · retains',
    'fixture-f-free · 33k · trains',
  ])
  expect(CATALOGUE_PROVIDERS).toEqual(['openrouter', 'zen'])
})

// The argv rules: the binary's own verbs, one argument per element.
// FAIL-first: with toggleEnabledArgv writing `set … true` to enable, the
// second expect reads ['…', 'set', 'providers.zen.enabled', 'true'].
test('setup pure: write argv — enable back is unset, an empty default is unset, odd ids stay one argument', () => {
  expect(toggleEnabledArgv(BIN, 'zen', true)).toEqual([BIN, 'config', 'set', 'providers.zen.enabled', 'false'])
  expect(toggleEnabledArgv(BIN, 'zen', false)).toEqual([BIN, 'config', 'unset', 'providers.zen.enabled'])
  expect(defaultModelArgv(BIN, 'zen', 'x')).toEqual([BIN, 'config', 'set', 'providers.zen.defaultModel', 'x'])
  expect(defaultModelArgv(BIN, 'zen', '')).toEqual([BIN, 'config', 'unset', 'providers.zen.defaultModel'])
  const odd = 'a b; rm -rf ~ $(id) `id` "q"'
  expect(defaultModelArgv(BIN, 'zen', odd)).toEqual([BIN, 'config', 'set', 'providers.zen.defaultModel', odd])
  expect(defaultModelArgv(BIN, 'zen', ' ')).toEqual([BIN, 'config', 'set', 'providers.zen.defaultModel', ' ']) // the binary judges it
  expect(catalogueArgv(BIN, 'zen')).toEqual([BIN, 'models', '--provider', 'zen', '--free', '--tools', '--json'])
  expect(setupRowsWanted(6)).toBe(12)
})

// The layout, top to bottom, and the open: the setup id and title, the rows
// the tree needs inline, no focus request. FAIL-first: with the blank Text
// drawn as '', the 8th line is missing; with `rows` left out of the open, the
// open reads without rows.
test('/rounds setup opens the setup pane: config line, one line per provider, controls, input', async ($, on) => {
  const { clock, state } = await boot($, on, [])
  expect(state.hints).toEqual(['[label] | send <label> <text> | wake [on|off] | on | off | setup'])
  const res = await $.command.run({ command: 'rounds', args: 'setup' })
  expect(res.text).toBeUndefined()
  expect(state.opens).toEqual([{ id: 'outsource-setup', title: 'outsource setup', rows: 12 }])
  expect(state.argvs).toEqual([[BIN, 'config', 'list', '--json']]) // loaded before the open
  const ui = await mountSetup($, 120)
  expect(await setupLines(ui)).toEqual([
    'config: ' + CONFIG_PATH,
    '› agy        on   launcher default',
    '  muse       off  launcher default',
    '  openrouter on   nvidia/nemotron-3-ultra-550b-a55b:free',
    '  xai        on   launcher default',
    '  zai        on   launcher default',
    '  zen        on   launcher default',
    ' ',
  ]) // no status line before any write
  expect((await ui.find({ type: 'Text', text: 'config: ' + CONFIG_PATH }))?.props.dimColor).toBe(true)
  expect((await ui.find({ type: 'Text', text: '  muse       off  ' }))?.props.dimColor).toBe(true)
  expect((await ui.find({ type: 'Text', text: '› agy        on   ' }))?.props.dimColor).toBe(false)
  const provider = await ui.find({ type: 'Select', key: 'provider' })
  expect(provider?.props.label).toBe('provider')
  expect(provider?.props.value).toBe('agy')
  expect((provider?.props.options as any[]).map((o) => o.value)).toEqual(['agy', 'muse', 'openrouter', 'xai', 'zai', 'zen'])
  expect((await ui.findAll({ type: 'Button' })).map((b: any) => b.props.label)).toEqual(['enabled: on', 'close']) // no reset: unset
  const input = await ui.find({ type: 'Input', key: 'model' })
  expect([input?.props.label, input?.props.submitLabel]).toEqual(['default model → agy', 'set'])
  expect(await ui.find({ type: 'Select', key: 'free' })).toBeUndefined() // agy is no catalogue

  // openrouter: its default is set, so reset shows; it is a catalogue, so the
  // free models follow the input — the focus order is controls, input, free.
  await ui.select({ key: 'provider', value: 'openrouter' })
  expect((await ui.findAll({ type: 'Button' })).map((b: any) => b.props.label)).toEqual(['enabled: on', 'reset default', 'close'])
  await ui.select({ key: 'provider', value: 'zen' })
  await clock.settle()
  const keys = allElements(await ui.drawn()).map((n: any) => n.props?.key).filter((k: any) => k !== undefined)
  expect(keys.indexOf('provider')).toBeLessThan(keys.indexOf('model'))
  expect(keys.indexOf('model')).toBeLessThan(keys.indexOf('free'))
  expect((await setupLines(ui))[6]).toBe('› zen        on   launcher default')
  await ui.unmount()
})

// Contract 2: every write's argv, recorded at process.run. FAIL-first: with
// writeConfig running ['/bin/sh', '-c', argv.join(' ')], the first expect
// reads undefined (no `config` argv reaches process.run).
test('setup: every write passes its argv exactly, as an array', async ($, on) => {
  const { clock, state } = await boot($, on, [])
  await $.command.run({ command: 'rounds', args: 'setup' })
  const ui = await mountSetup($, 120)
  await ui.press({ key: 'enabled' }) // agy on → off
  expect(writesOf(state).at(-1)).toEqual([BIN, 'config', 'set', 'providers.agy.enabled', 'false'])
  expect(await ui.find({ type: 'Button', key: 'enabled' })).toMatchObject({ props: { label: 'enabled: off' } })
  await ui.press({ key: 'enabled' }) // off → on again: unset, not `set true`
  expect(writesOf(state).at(-1)).toEqual([BIN, 'config', 'unset', 'providers.agy.enabled'])
  await ui.input({ key: 'model', text: 'glm-5.3' })
  expect(writesOf(state).at(-1)).toEqual([BIN, 'config', 'set', 'providers.agy.defaultModel', 'glm-5.3'])
  await ui.press({ key: 'reset' })
  expect(writesOf(state).at(-1)).toEqual([BIN, 'config', 'unset', 'providers.agy.defaultModel'])
  await ui.input({ key: 'model', text: 'glm-5.3' })
  await ui.input({ key: 'model', text: '' }) // an empty submit unsets
  expect(writesOf(state).at(-1)).toEqual([BIN, 'config', 'unset', 'providers.agy.defaultModel'])
  await ui.select({ key: 'provider', value: 'zen' })
  await clock.settle()
  await ui.select({ key: 'free', value: 'fixture-a-free' }) // a free-model pick sets it
  expect(writesOf(state).at(-1)).toEqual([BIN, 'config', 'set', 'providers.zen.defaultModel', 'fixture-a-free'])
  expect((await ui.find({ type: 'Select', key: 'free' }))?.props.value).toBe('fixture-a-free')
  const odd = 'x y; touch /tmp/pwned $(id) `id`'
  await ui.input({ key: 'model', text: odd })
  expect(writesOf(state).at(-1)).toEqual([BIN, 'config', 'set', 'providers.zen.defaultModel', odd])
  expect(writesOf(state)).toHaveLength(8)
  expect(state.argvs.every((a) => a[0] === BIN)).toBe(true) // never a shell
  await ui.unmount()
})

// Contract 3: after a write the pane reloads `config list` and draws what the
// binary now holds; a refused write leaves the state and shows the binary's
// first stderr line, red. FAIL-first: without the reload after a write, the
// list count stays (Expected 2, Received 1); with the status line drawn from
// stdout on a refusal, it is empty.
test('setup: a write reloads and draws the new state; a refused write keeps it and shows stderr', async ($, on) => {
  const { clock, state } = await boot($, on, [])
  await $.command.run({ command: 'rounds', args: 'setup' })
  const ui = await mountSetup($, 120)
  const before = listsOf(state)
  await ui.press({ key: 'enabled' })
  expect(listsOf(state)).toBe(before + 1)
  const lines = await setupLines(ui)
  expect(lines[1]).toBe('› agy        off  launcher default')
  expect((await ui.find({ type: 'Text', text: '› agy        off  ' }))?.props.dimColor).toBe(true)
  expect(lines.at(-1)).toBe('providers.agy.enabled = false (' + CONFIG_PATH + ')') // the binary's own line
  const ok = await ui.find({ type: 'Text', text: 'providers.agy.enabled = false (' + CONFIG_PATH + ')' })
  expect([ok?.props.dimColor, ok?.props.color]).toEqual([true, undefined])

  const refusal =
    'outsource config: providers.zen.defaultModel: a default model is stored bare and the launcher adds the opencode/ qualifier itself, so "opencode/x" would reach the harness as opencode/opencode/x — store "x"'
  state.configRefusal = { exitCode: 64, stderr: refusal + '\nsecond line\n' }
  await ui.select({ key: 'provider', value: 'zen' })
  await clock.settle()
  const held = JSON.stringify(state.config.values)
  const listsBefore = listsOf(state)
  await ui.input({ key: 'model', text: 'opencode/x' })
  expect(writesOf(state).at(-1)).toEqual([BIN, 'config', 'set', 'providers.zen.defaultModel', 'opencode/x'])
  expect(JSON.stringify(state.config.values)).toBe(held)
  expect(listsOf(state)).toBe(listsBefore + 1)
  const after = await setupLines(ui)
  expect(after).toContain('› zen        on   launcher default') // the state stays
  expect(after.at(-1)).toBe(truncate(refusal, 120))
  const status = await ui.find({ type: 'Text', text: truncate(refusal, 120) })
  expect(status?.props.color).toBe('red')
  expect(state.logs).toContain(logText('config set providers.zen.defaultModel opencode/x → exit 64'))
  await ui.unmount()
})

// Contract 4: every process call is in a handler; a render reads state only.
// FAIL-first: with the render hook calling loadConfig, processCalls grows
// with every redraw.
test('setup: no process runs inside a render', async ($, on) => {
  const { clock, state } = await boot($, on, [])
  await $.command.run({ command: 'rounds', args: 'setup' })
  expect(listsOf(state)).toBe(1) // the open loaded
  const first = await mountSetup($, 120)
  await first.select({ key: 'provider', value: 'zen' })
  await clock.settle()
  expect(modelsOf(state)).toHaveLength(1) // the pick fetched
  const before = state.processCalls
  await first.redraw()
  await first.redraw()
  await first.redraw({ ...SETUP_PROPS(72, 20) })
  await first.unmount()
  const second = await mountSetup($, 40)
  await second.redraw()
  await clock.settle()
  expect(state.processCalls).toBe(before)
  await second.unmount()
})

// Contract 5: one outsource pane at a time; each command toggles its own.
// FAIL-first: without the close in openSetup, the rounds pane stays open
// beside it (closes reads []).
test('setup: one outsource pane at a time', async ($, on) => {
  const { clock, state } = await boot($, on, fixtureRows)
  const ids = (list: any[]) => list.map((x) => x.id)
  await $.command.run({ command: 'rounds', args: '' }) // rounds open
  await $.command.run({ command: 'rounds', args: 'setup' })
  expect(ids(state.closes)).toEqual(['outsource-rounds'])
  expect(ids(state.opens)).toEqual(['outsource-rounds', 'outsource-setup'])
  const polls = state.runsCalls
  await clock.advance(10000) // the rounds timer is back at the closed cadence
  expect(state.runsCalls).toBe(polls + 2)

  await $.command.run({ command: 'rounds', args: '' }) // /rounds closes setup and opens rounds
  expect(ids(state.closes)).toEqual(['outsource-rounds', 'outsource-setup'])
  expect(ids(state.opens)).toEqual(['outsource-rounds', 'outsource-setup', 'outsource-rounds'])

  await $.command.run({ command: 'rounds', args: '' }) // toggles rounds closed
  await $.command.run({ command: 'rounds', args: 'setup' }) // opens setup, nothing to close
  expect(ids(state.closes)).toEqual(['outsource-rounds', 'outsource-setup', 'outsource-rounds'])
  await $.command.run({ command: 'rounds', args: 'setup' }) // again: closes it
  expect(ids(state.closes)).toEqual(['outsource-rounds', 'outsource-setup', 'outsource-rounds', 'outsource-setup'])
  expect(ids(state.opens)).toEqual(['outsource-rounds', 'outsource-setup', 'outsource-rounds', 'outsource-setup'])

  await $.command.run({ command: 'rounds', args: 'setup' })
  await $.command.run({ command: 'rounds', args: 'quota-report' }) // a label opens rounds: setup closes
  expect(ids(state.closes).at(-1)).toBe('outsource-setup')
  expect(ids(state.opens).at(-1)).toBe('outsource-rounds')
})

// Contract 6: `models` only for a catalogue provider, once per provider per
// open, with the long timeout; the config calls keep the short one.
// FAIL-first: without the once-per-open guard, the second zen pick fetches
// again (two zen calls); with the catalogue test dropped, the agy open calls
// `models --provider agy`.
test('setup: the catalogue is fetched only for catalogue providers, once per provider per open', async ($, on) => {
  const { clock, state } = await boot($, on, [])
  await $.command.run({ command: 'rounds', args: 'setup' })
  const ui = await mountSetup($, 120)
  for (const p of ['muse', 'xai', 'zai', 'agy']) await ui.select({ key: 'provider', value: p })
  await clock.settle()
  expect(modelsOf(state)).toEqual([])
  expect(await ui.find({ type: 'Select', key: 'free' })).toBeUndefined()

  await ui.select({ key: 'provider', value: 'zen' })
  await clock.settle()
  expect(modelsOf(state)).toEqual([[BIN, 'models', '--provider', 'zen', '--free', '--tools', '--json']])
  const at = state.argvs.findIndex((a) => a[1] === 'models')
  expect(state.inits[at]).toEqual({ timeoutMs: 30000 })
  state.argvs.forEach((a, i) => {
    if (a[1] === 'config') expect(state.inits[i]).toEqual({ timeoutMs: 4000 })
  })
  const free = await ui.find({ type: 'Select', key: 'free' })
  expect(free?.props.label).toBe('free models')
  expect((free?.props.options as any[])[0]).toEqual({ value: 'nemotron-3-ultra-free', label: 'nemotron-3-ultra-free · 1000k · trains' })
  expect(free?.props.options as any[]).toHaveLength(8)

  await ui.select({ key: 'provider', value: 'agy' })
  await ui.select({ key: 'provider', value: 'zen' })
  await clock.settle()
  expect(modelsOf(state)).toHaveLength(1) // once per open

  await ui.select({ key: 'provider', value: 'openrouter' }) // no catalogue served: exit 1
  await clock.settle()
  expect(modelsOf(state).map((a) => a[3])).toEqual(['zen', 'openrouter'])
  const line = await ui.find({ type: 'Text', text: 'free models unavailable: models: openrouter: no catalogue loaded' })
  expect(line?.props.dimColor).toBe(true)
  await ui.select({ key: 'provider', value: 'openrouter' })
  await clock.settle()
  expect(modelsOf(state)).toHaveLength(2) // a failure is not retried within the open

  await $.command.run({ command: 'rounds', args: 'setup' }) // close
  await $.command.run({ command: 'rounds', args: 'setup' }) // reopen: openrouter still selected, fetched again
  await clock.settle()
  expect(modelsOf(state).map((a) => a[3])).toEqual(['zen', 'openrouter', 'openrouter'])
  await ui.unmount()
})

// The catalogue's three other faces: loading while `models` runs, a timeout
// (the run rejects), unparseable output, and no option at all (a Select
// takes at least one). FAIL-first: with the loading entry left out of
// startCatalogue, the pick made while the first fetch is held fetches again
// (modelsHeld length 2).
test('setup: catalogue loading, timeout, unparseable and empty lines', async ($, on) => {
  const { clock, state } = await boot($, on, [])
  state.modelsHeld = []
  await $.command.run({ command: 'rounds', args: 'setup' })
  const ui = await mountSetup($, 120)
  await ui.select({ key: 'provider', value: 'zen' })
  expect(state.modelsHeld).toHaveLength(1) // the pick did not wait for it
  await ui.select({ key: 'provider', value: 'agy' })
  await ui.select({ key: 'provider', value: 'zen' })
  expect(state.modelsHeld).toHaveLength(1) // in flight counts as fetched
  const loading = await ui.find({ type: 'Text', text: 'loading free models…' })
  expect(loading?.props.dimColor).toBe(true)
  expect(await ui.find({ type: 'Select', key: 'free' })).toBeUndefined()
  state.modelsHeld.shift()!()
  await clock.settle()
  expect(await ui.find({ type: 'Select', key: 'free' })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: 'loading free models…' })).toBeUndefined()

  const reopen = async () => {
    await $.command.run({ command: 'rounds', args: 'setup' })
    await $.command.run({ command: 'rounds', args: 'setup' })
    await clock.settle()
  }
  state.modelsHeld = null
  state.modelsReject = 'timed out after 30000 ms'
  await reopen()
  // The engine's rejection reads `<plugin>: $.process.run: <reason>`; the
  // pane drops the plugin name (rejectionText), which the log line's one
  // prefix already carries (the last test checks it).
  const timedOut = '$.process.run: timed out after 30000 ms'
  expect(await ui.find({ type: 'Text', text: 'free models unavailable: ' + timedOut })).toBeDefined()
  state.modelsReject = null
  state.models.zen = '{"models": nope'
  await reopen()
  expect(await ui.find({ type: 'Text', text: 'free models unavailable: unparseable output' })).toBeDefined()
  state.models.zen = JSON.stringify({ ...MODELS_ZEN, models: [] })
  await reopen()
  expect(await ui.find({ type: 'Text', text: 'free models: none' })).toBeDefined()
  expect(await ui.find({ type: 'Select', key: 'free' })).toBeUndefined()
  expect(state.logs).toContain(logText('free models for zen unavailable: ' + timedOut))
  await ui.unmount()
})

// Nothing loaded yet draws `loading…`; a failed `config list` draws its
// reason red, and under a later failure the last good state stays.
// FAIL-first: with the loading line removed, the first expect reads [].
test('setup: loading… until a load; a failed config list draws its reason', async ($, on) => {
  const { state } = await boot($, on, [])
  const ui = await mountSetup($, 120) // drawn before any open
  expect(await setupLines(ui)).toEqual(['loading…'])
  expect(listsOf(state)).toBe(0)
  state.listFails = { exitCode: 1, stderr: 'outsource config: /tmp/x/config.json: invalid character \'}\'\nmore\n' }
  await $.command.run({ command: 'rounds', args: 'setup' })
  expect(await setupLines(ui)).toEqual(["config list failed: outsource config: /tmp/x/config.json: invalid character '}'"])
  expect((await ui.find({ type: 'Text', text: /^config list failed: / }))?.props.color).toBe('red')
  expect(await ui.findAll({ type: 'Button' })).toEqual([])

  state.listFails = null
  state.listStdout = 'not json'
  await $.command.run({ command: 'rounds', args: 'setup' })
  await $.command.run({ command: 'rounds', args: 'setup' })
  expect(await setupLines(ui)).toEqual(['config list failed: unparseable output'])

  state.listStdout = null
  await $.command.run({ command: 'rounds', args: 'setup' })
  await $.command.run({ command: 'rounds', args: 'setup' })
  expect((await setupLines(ui))[0]).toBe('config: ' + CONFIG_PATH)
  state.listFails = { exitCode: 1, stderr: 'outsource config: read: permission denied\n' }
  await ui.press({ key: 'enabled' }) // the write goes through; the reload after it fails
  const lines = await setupLines(ui)
  expect(lines[0]).toBe('config list failed: outsource config: read: permission denied')
  expect(lines[1]).toBe('config: ' + CONFIG_PATH) // the last good state stays drawn
  expect(lines[2]).toBe('› agy        on   launcher default')
  await ui.unmount()
})

// Binary missing: the red line alone, no controls, no process. FAIL-first:
// without the render's binOk branch, the pane draws `loading…`.
test('setup: binary missing draws the red line and no controls', async ($, on) => {
  const { state } = await boot($, on, fixtureRows, { exists: () => false })
  await $.command.run({ command: 'rounds', args: 'setup' })
  expect(state.argvs).toEqual([])
  expect(state.opens.map((o: any) => o.id)).toEqual(['outsource-setup'])
  const ui = await mountSetup($, 600)
  expect(await texts(ui)).toEqual(['outsource binary not found: ' + state.existsAsked.join(' · ')])
  expect((await ui.find({ type: 'Text' }))?.props.color).toBe('red')
  for (const type of ['Button', 'Select', 'Input']) expect(await ui.findAll({ type })).toEqual([])
  await ui.unmount()
})

// The panel switch covers the setup pane like everything else, and `setup`
// is a subcommand word. FAIL-first: without the setup close in setEnabled,
// closes reads [] after `/rounds off`.
test('setup: panel off answers the off line and closes the setup pane; extra words are usage', async ($, on) => {
  const { state } = await boot($, on, [])
  await $.command.run({ command: 'rounds', args: 'setup' })
  await $.command.run({ command: 'rounds', args: 'off' })
  expect(state.closes.map((c: any) => c.id)).toEqual(['outsource-setup'])
  const lists = listsOf(state)
  expect((await $.command.run({ command: 'rounds', args: 'setup' })).text).toBe(PANEL_OFF_TEXT)
  expect(state.opens).toHaveLength(1)
  expect(listsOf(state)).toBe(lists)
  const pane = await mountSetup($, 120)
  expect(await texts(pane)).toEqual([]) // off: the render passes on
  await pane.unmount()
  await $.command.run({ command: 'rounds', args: 'on' })
  expect((await $.command.run({ command: 'rounds', args: 'setup now' })).text).toBe('usage: /rounds setup')
  expect(state.opens).toHaveLength(1)
})

// No focus capture in the setup pane either. FAIL-first: with `focus: true`
// in openSetup's open, the `focus` expect fails.
test('setup: no autoFocus, hotkey or focus request', async ($, on) => {
  const { clock, state } = await boot($, on, [])
  await $.command.run({ command: 'rounds', args: 'setup' })
  const ui = await mountSetup($, 120)
  await ui.select({ key: 'provider', value: 'zen' })
  await clock.settle()
  await ui.press({ key: 'enabled' }) // a status line too
  const nodes = allElements(await ui.drawn())
  expect(nodes.some((n: any) => n.type === 'Select' && n.props?.key === 'free')).toBe(true)
  for (const node of nodes) {
    expect(node.props?.autoFocus, `${node.type} autoFocus`).toBeUndefined()
    expect(node.props?.hotkey, `${node.type} hotkey`).toBeUndefined()
  }
  expect('focus' in state.opens[0]).toBe(false)
  await ui.unmount()
})

// Every line fits its body, a provider line's two Texts together included.
// FAIL-first: with the status line drawn uncut, the 30-column check fails.
test('setup: every line fits bodyColumns at 30, 40 and 72', async ($, on) => {
  const { clock, state } = await boot($, on, [])
  state.configRefusal = { exitCode: 64, stderr: 'outsource config: ' + 'x'.repeat(200) + '\n' }
  await $.command.run({ command: 'rounds', args: 'setup' })
  const probe = await mountSetup($, 120)
  await probe.select({ key: 'provider', value: 'openrouter' })
  await clock.settle()
  await probe.press({ key: 'enabled' }) // a long red status line
  await probe.unmount()
  for (const columns of [30, 40, 72]) {
    const ui = await mountSetup($, columns)
    const lines = await setupLines(ui)
    expect(lines.length).toBeGreaterThan(8)
    for (const line of lines) expect(displayWidth(line), `setup ${columns}: ${JSON.stringify(line)}`).toBeLessThanOrEqual(columns)
    for (const text of await texts(ui)) expect(displayWidth(text)).toBeLessThanOrEqual(columns)
    await ui.unmount()
  }
})

// An open the engine cannot seat answers the setup lines as command text, as
// the rounds pane does (test 12). FAIL-first: with toggleSetup ignoring
// isPlaced, res.text is undefined.
test('setup: a not-placed open returns the setup lines as text', async ($, on) => {
  const reason = 'no room above the prompt'
  await boot($, on, [], { uiOpen: { isPlaced: false, reason } })
  const res = await $.command.run({ command: 'rounds', args: 'setup' })
  expect((res.text ?? '').split('\n')).toEqual([
    'config: ' + CONFIG_PATH,
    '› agy        on   launcher default',
    '  muse       off  launcher default',
    '  openrouter on   nvidia/nemotron-3-ultra-550b-a55b:free',
    '  xai        on   launcher default',
    '  zai        on   launcher default',
    '  zen        on   launcher default',
    '(pane not placed: ' + reason + ')',
  ])
})

// A reload that finds the setup pane open loads it (it has no timer).
// FAIL-first: without the load in session.start, no config list runs
// (Expected 1, Received 0).
test('setup: a reload that finds the setup pane open loads it', async ($, on) => {
  const { clock, state } = await boot($, on, [], { panes: [{ id: 'outsource-setup' }] })
  await clock.settle()
  expect(listsOf(state)).toBe(1)
  const ui = await mountSetup($, 120)
  expect((await setupLines(ui))[0]).toBe('config: ' + CONFIG_PATH)
  await $.command.run({ command: 'rounds', args: 'setup' }) // the engine's record says open: this closes it
  expect(state.closes.map((c: any) => c.id)).toEqual(['outsource-setup'])
  await ui.unmount()
})

// Last, on purpose: every `$.ui.log` line any test above produced went to the
// debug log with exactly one `outsource-panel: ` prefix. FAIL-first: with one
// site logging to the transcript, its line is listed here.
test('every $.ui.log above went to the debug log, prefixed once', () => {
  expect(LOG_VIOLATIONS).toEqual([])
})
