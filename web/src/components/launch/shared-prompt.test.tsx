import { act, renderHook } from '@testing-library/react'
import { toast } from 'sonner'
import { useSharedPrompt } from '@/components/launch/shared-prompt'
import { initialRoute, takeSharedPrompt } from '@/lib/url-state'
import { useStore } from '@/store'
import { serverInfo } from '@/test/fixtures'

vi.mock('sonner', () => ({ toast: { error: vi.fn(), warning: vi.fn() } }))

function share(query: string) {
  window.history.replaceState(null, '', `/?token=t&${query}`)
  initialRoute()
}

beforeEach(() => {
  takeSharedPrompt()
  vi.clearAllMocks()
  useStore.setState({
    hydrated: true,
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    paletteDialog: null,
    paletteLaunchTask: '',
  })
})

it('takes the shared text off the address at load and opens New session with it', () => {
  share('new=1&prompt=fix%20the%20login')
  expect(window.location.search).toBe('?token=t')
  renderHook(() => useSharedPrompt())
  expect(useStore.getState().paletteDialog).toBe('launch')
  expect(useStore.getState().paletteLaunchTask).toBe('fix the login')
})

it('drops a prompt without new=1', () => {
  share('prompt=hi')
  expect(window.location.search).toBe('?token=t')
  renderHook(() => useSharedPrompt())
  expect(useStore.getState().paletteDialog).toBeNull()
})

it('keeps the first 8000 characters and says it cut the rest', () => {
  share(`new=1&prompt=${'a'.repeat(8001)}`)
  renderHook(() => useSharedPrompt())
  expect(useStore.getState().paletteLaunchTask).toHaveLength(8000)
  expect(toast.warning).toHaveBeenCalledWith('Shared text cut to its first 8000 characters, of 8001.')
})

it('names the role that cannot launch', () => {
  useStore.setState({ info: { ...serverInfo, member: { ...serverInfo.member, role: 'viewer' } } })
  share('new=1&prompt=hi')
  renderHook(() => useSharedPrompt())
  expect(useStore.getState().paletteDialog).toBeNull()
  expect(toast.error).toHaveBeenCalledWith('Shared text not opened: a viewer cannot launch sessions.')
})

it('names a gateway without run.launch', () => {
  useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.list'], ws: [] } })
  share('new=1&prompt=hi')
  renderHook(() => useSharedPrompt())
  expect(toast.error).toHaveBeenCalledWith('Shared text not opened: this gateway does not offer run.launch, so it cannot start sessions.')
})

it('waits for an open form to close', () => {
  useStore.setState({ paletteDialog: 'inject' })
  share('new=1&prompt=hi')
  renderHook(() => useSharedPrompt())
  expect(useStore.getState().paletteDialog).toBe('inject')
  act(() => useStore.getState().closePaletteDialog())
  expect(useStore.getState().paletteDialog).toBe('launch')
})

it('forgets shared text when the signed-in member changes', () => {
  useStore.setState({ identityKey: 'ana' })
  useStore.getState().openLaunchWithTask('from ana')
  useStore.getState().setIdentityKey('ben')
  expect(useStore.getState().paletteLaunchTask).toBe('')
})
