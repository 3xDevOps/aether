import { enhancedBlock, pillFor } from '@/routes/run/composer-state'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

describe('the composer pill', () => {
  const base = { paused: false, turnRunning: false, steering: true, modHeld: false, empty: false }
  it.each([
    [{}, 'send'],
    [{ turnRunning: true }, 'steer'],
    [{ turnRunning: true, modHeld: true }, 'queue'],
    [{ turnRunning: true, steering: false }, 'queue'],
    [{ turnRunning: true, empty: true }, 'interrupt'],
    [{ paused: true, turnRunning: true }, 'resume'],
  ] as const)('%j reads %s', (over, pill) => {
    expect(pillFor({ ...base, ...over })).toBe(pill)
  })
})

describe('the Enhanced composer gate', () => {
  const open = {
    run: toRecord(run({ mode: 'acp', acp: true })),
    maySteer: true,
    canReopen: true,
    stream: 'live' as const,
    sessionLive: true,
    pending: 0,
    hasLease: true,
    controller: null,
  }
  it.each([
    [{}, null],
    [{ run: toRecord(run({ mode: 'acp', acp: true, switching: 'tui' })) }, 'Switching to Standard…'],
    [{ run: toRecord(run({ mode: 'headless', acp: true })) }, 'Background runs take no input.'],
    [{ stream: 'connecting' as const }, 'Connecting to the agent…'],
    [{ sessionLive: false }, 'The agent’s session is not running.'],
    [{ pending: 1 }, 'Answer the request above to continue.'],
    [{ hasLease: false }, 'Take control to message the agent.'],
    [{ hasLease: false, controller: 'self' as const }, 'You control this run in another tab.'],
    [{ hasLease: false, controller: 'other' as const }, 'Someone else controls this run.'],
  ])('%j', (over, reason) => {
    expect(enhancedBlock({ ...open, ...over })?.reason ?? null).toBe(reason)
  })
})
