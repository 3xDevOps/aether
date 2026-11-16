import { openLaunchPopout, launchPopoutURL, receiveLaunchPopoutMessage, sendLaunchPopoutDiscarded, sendLaunchPopoutReturned } from '@/lib/launch-popout'
import { parseLaunchDraft, type LaunchDraft } from '@/components/launch/draft'

const draft: LaunchDraft = {
  version: 1,
  id: 'launch-draft-1',
  workspace: 'workspace-1',
  extras: [],
  account: 'member-1',
  fallback: { off: false, order: null, excluded: [] },
  permission: 'default',
  childRuns: '',
  limits: { cpus: '', memory: '', pids: '' },
  harness: 'claude',
  mode: 'acp',
  model: '',
  effort: '',
  task: 'fix the launch flow',
}

afterEach(() => vi.restoreAllMocks())

it('opens a clean launch URL with the complete draft in its fragment', () => {
  const url = new URL(launchPopoutURL('http://localhost/?run=run_1&view=changes&prompt=secret', draft, 'gateway-token'))
  expect(url.searchParams.get('token')).toBe('gateway-token')
  expect(url.searchParams.get('run')).toBeNull()
  expect(url.searchParams.get('prompt')).toBeNull()
  expect(url.searchParams.get('new')).toBe('1')
  expect(url.searchParams.get('popout')).toBe('1')
  expect(new URLSearchParams(url.hash.slice(1)).get('launch-draft')).toBe(JSON.stringify(draft))
  expect(parseLaunchDraft(JSON.stringify(draft))).toEqual(draft)
  expect(parseLaunchDraft('{"version":2}')).toBeNull()
})

it('tracks only launch windows opened by this page and accepts their same-origin results', () => {
  const popup = { closed: false } as unknown as WindowProxy
  vi.spyOn(window, 'open').mockReturnValue(popup)
  expect(openLaunchPopout(window.location.href, draft)).toBe(true)

  const data = { source: 'aether-launch-popout', type: 'launched', draftID: draft.id, runID: 'run_2' }
  const wrongOrigin = new MessageEvent('message', { origin: 'https://example.com', source: popup as unknown as Window, data })
  expect(receiveLaunchPopoutMessage(wrongOrigin)).toBeNull()

  const accepted = new MessageEvent('message', { origin: window.location.origin, source: popup as unknown as Window, data })
  expect(receiveLaunchPopoutMessage(accepted)).toEqual(data)
  expect(receiveLaunchPopoutMessage(accepted)).toBeNull()
})

it('returns the edited draft when the pop-out is closed', () => {
  const popup = {} as WindowProxy
  vi.spyOn(window, 'open').mockReturnValue(popup)
  openLaunchPopout(window.location.href, draft)
  const data = { source: 'aether-launch-popout', type: 'returned', draft }
  const event = new MessageEvent('message', { origin: window.location.origin, source: popup as unknown as Window, data })

  expect(receiveLaunchPopoutMessage(event)).toEqual(data)
  expect(receiveLaunchPopoutMessage(event)).toBeNull()
})

it('notifies the opener and closes when the pop-out is discarded', () => {
  const previous = Object.getOwnPropertyDescriptor(window, 'opener')
  const opener = { closed: false, postMessage: vi.fn() }
  const close = vi.spyOn(window, 'close').mockImplementation(() => {})
  Object.defineProperty(window, 'opener', { configurable: true, value: opener })
  try {
    sendLaunchPopoutDiscarded(draft.id)
    expect(opener.postMessage).toHaveBeenCalledWith(
      { source: 'aether-launch-popout', type: 'discarded', draftID: draft.id },
      window.location.origin,
    )
    expect(close).toHaveBeenCalledOnce()
  } finally {
    if (previous) Object.defineProperty(window, 'opener', previous)
    else delete (window as Window & { opener?: Window }).opener
  }
})

it('sends the final draft to the opener before a normal close', () => {
  const previous = Object.getOwnPropertyDescriptor(window, 'opener')
  const opener = { closed: false, postMessage: vi.fn() }
  Object.defineProperty(window, 'opener', { configurable: true, value: opener })
  try {
    sendLaunchPopoutReturned(draft)
    expect(opener.postMessage).toHaveBeenCalledWith(
      { source: 'aether-launch-popout', type: 'returned', draft },
      window.location.origin,
    )
  } finally {
    if (previous) Object.defineProperty(window, 'opener', previous)
    else delete (window as Window & { opener?: Window }).opener
  }
})

it('leaves the current form open when the browser blocks the pop-up', () => {
  vi.spyOn(window, 'open').mockReturnValue(null)
  expect(openLaunchPopout(window.location.href, draft)).toBe(false)
})
