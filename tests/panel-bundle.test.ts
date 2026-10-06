// The bundled-first half of "never two panels", run by `claude plugin test`
// against the repo root (tests/panel-mod.test.sh), where the plugin under test
// is the root plugin `outsource` that carries the panel module. It lives here,
// outside mods/outsource-panel, because the standalone run must not see it:
// there the plugin under test is `outsource-panel`, and a stand-in of that
// name cannot load beside it.
//
// The standalone-first half is the `pair:` test in
// mods/outsource-panel/tests/panel.test.ts (it runs in the standalone run).

import { test, expect } from 'claude-code/testing'
import { PANEL_NAME, logText } from '../mods/outsource-panel/hooks/view.js'

// A stand-in for the standalone copy: what `outsource-panel` would register at
// session.start. An inline plugin closes over nothing of this file, so it
// cannot load the real module — this simulates the other copy; the live proof
// is the M4 capture.
const PANEL_STANDIN = {
  name: 'outsource-panel',
  register(on: any) {
    on('session.start', async ($: any, e: any, next: any) => {
      await $.command.register({ name: 'rounds', description: 'stand-in for the standalone panel' })
      await $.tool.register({ name: 'rounds', description: 'stand-in for the standalone panel' })
      return next(e)
    })
  },
}

// An inline plugin of the user tier is admitted after the plugin under test,
// so the bundled copy judges the standalone's admission and refuses it: the
// standalone never joins — no hook, no command, no tool — and the refusal is
// one debug line. FAIL-first: without the refusal the stand-in loads,
// session.start resolves, and the stand-in's command and tool are registered.
test('pair: the bundled copy refuses a standalone admitted after it', { plugins: [PANEL_STANDIN] }, async ($, on) => {
  const logs: Array<{ origin: string; text: string; to: string }> = []
  const registered: string[] = []
  on('session.start', () => ({ cwd: '/tmp/panel-test' }))
  on('ui.log', ($, e: any, next: any) => {
    logs.push({ origin: next.origin.plugin, text: e.text, to: e.to })
    return { value: undefined }
  })
  on('command.register', ($, e: any, next: any) => {
    registered.push(next.origin.plugin + ':' + e.name)
    return { value: { command: e.name } }
  })
  on('tool.register', ($, e: any, next: any) => {
    registered.push(next.origin.plugin + ':' + e.name)
    return { value: { tool: 'mcp__' + next.origin.plugin + '__' + e.name } }
  })

  let refusal = ''
  try {
    await $.session.start({ cwd: '/tmp/panel-test', surface: 'terminal', isInteractive: true })
  } catch (err) {
    refusal = String(err)
  }
  expect(refusal).toContain(PANEL_NAME + ': refused by outsource: the bundled outsource plugin carries the panel in this session')
  expect(registered).toEqual([]) // the stand-in never ran
  expect(logs).toHaveLength(1)
  expect(logs[0].origin).toBe('outsource')
  expect(logs[0].to).toBe('debug')
  const head = logText('refused the standalone outsource-panel (')
  expect(logs[0].text.startsWith(head)).toBe(true)
  expect(logs[0].text.endsWith('): this plugin carries the panel')).toBe(true)
})
