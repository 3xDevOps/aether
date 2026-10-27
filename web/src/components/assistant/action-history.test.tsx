import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ActionHistory } from '@/components/assistant/action-history'
import type * as actionsModule from '@/lib/assistant-actions'
import { assistantView } from '@/lib/assistant-actions'
import { useStore } from '@/store'
import type { AcpSession } from '@/store/sessions'

vi.mock('@/lib/assistant-actions', async (importOriginal) => {
  const actual = await importOriginal<typeof actionsModule>()
  return { ...actual, assistantView: vi.fn(actual.assistantView) }
})

const session = () => ({ turns: [], live: false, more: false, olderLoading: false }) as unknown as AcpSession

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

it('reads the transcript only while the list is open', async () => {
  useStore.setState(useStore.getInitialState(), true)
  const { rerender } = render(<ActionHistory session={session()} />)
  for (let i = 0; i < 3; i++) rerender(<ActionHistory session={session()} />)
  expect(assistantView).not.toHaveBeenCalled()

  await userEvent.click(screen.getByRole('button', { name: 'Actions' }))
  expect(await screen.findByText('No actions yet. Reads are not listed.')).toBeDefined()
  const opened = vi.mocked(assistantView).mock.calls.length
  expect(opened).toBeGreaterThan(0)
  rerender(<ActionHistory session={session()} />)
  expect(vi.mocked(assistantView).mock.calls.length).toBeGreaterThan(opened)
})
