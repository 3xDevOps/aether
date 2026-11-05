import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { AppShell } from '@/components/shell/app-shell'
import { api } from '@/lib/api'
import type { AttentionItem, RoomPostResult } from '@/lib/types'
import { useAttention } from '@/routes/needs-you/attention'
import { useStore } from '@/store'
import { hydrate } from '@/store/sync'
import { fakeApi, roomMessage, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

Element.prototype.scrollIntoView = vi.fn()

const base = { workspace_id: workspace.id, harness: 'claude', since: '2026-08-14T10:02:00Z' }

async function showNeedsYou(items: AttentionItem[]) {
  atViewport(390, { pointer: 'coarse' })
  useStore.getState().setOnboarded(true)
  await hydrate(useStore, fakeApi({ workspaceListFull: vi.fn(async () => [workspace]) }))
  useStore.setState({
    runs: {},
    route: { name: 'needs-you', params: {} },
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events'], local: [] },
  })
  const list = vi.spyOn(api, 'attentionList').mockResolvedValue({ items })
  onTestFinished(() => {
    list.mockRestore()
    useAttention.setState({ items: null })
  })
  render(<AppShell />)
  return within(await screen.findByRole('list', { name: 'Needs you' }))
}

describe('Needs you cards', () => {
  it('replies to a room question with run.room.post, correlated to the question', async () => {
    const posted: RoomPostResult = { message: roomMessage({ kind: 'reply', body: 'Use port 8080', state: 'sent' }) }
    const post = vi.spyOn(api, 'runRoomPost').mockResolvedValue(posted)
    onTestFinished(() => post.mockRestore())
    const cards = await showNeedsYou([{
      ...base, kind: 'room-question', run_id: 'run_q', title: 'pick a port', reason: 'Bob asked you: Which port?',
      message: { id: 'msg_q', body: 'Which port?', from: 'mem_bob' },
    }])

    fireEvent.click(cards.getByRole('button', { name: 'Reply…' }))
    const field = cards.getByRole('textbox', { name: 'Reply to pick a port' })
    fireEvent.change(field, { target: { value: 'Use port 8080' } })
    fireEvent.keyDown(field, { key: 'Enter', ctrlKey: true })

    await waitFor(() => expect(post).toHaveBeenCalledOnce())
    expect(post.mock.calls[0]![0]).toMatchObject({ run_id: 'run_q', kind: 'reply', body: 'Use port 8080', correlation_id: 'msg_q' })
    // The question's text is the reason already, so it is not repeated below it.
    expect(cards.getAllByText(/Which port\?/)).toHaveLength(1)
  })

  it('answers an Enhanced question form, sending values only with Accept', async () => {
    const respond = vi.spyOn(api, 'runInputRespond').mockResolvedValue({ status: 'answered' })
    onTestFinished(() => respond.mockRestore())
    const cards = await showNeedsYou([{
      ...base, kind: 'question', run_id: 'run_f', title: 'set up the database', reason: 'Question from the agent',
      request: {
        id: 'req_f', kind: 'question', title: 'Which database?',
        options: [{ id: 'accept', label: 'Accept' }, { id: 'decline', label: 'Decline' }],
        schema: { type: 'object', properties: { db: { type: 'string' } }, required: ['db'] },
      },
    }])

    const accept = cards.getByRole('button', { name: 'Accept' })
    expect(accept).toHaveProperty('disabled', true)
    fireEvent.change(cards.getByRole('textbox', { name: /db/ }), { target: { value: 'postgres' } })
    fireEvent.click(accept)

    await waitFor(() => expect(respond).toHaveBeenCalledWith('run_f', 'req_f', 'accept', { db: 'postgres' }))
    // A second tap could only be refused, so the answered card offers none.
    expect(await cards.findByText('Answered')).toBeDefined()
    expect(cards.queryByRole('button', { name: 'Decline' })).toBeNull()
  })
})
