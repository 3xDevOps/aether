import { act, fireEvent, render, screen } from '@testing-library/react'
import { usePromptHistory } from '@/routes/run/composer-history'
import { saveComposerDraft, useComposerDraft } from '@/routes/run/composer-state'
import { useStore } from '@/store'
import { alice, roomMessage, serverInfo } from '@/test/fixtures'

const runID = 'run_history'

function sent(id: string, body: string, over: Parameters<typeof roomMessage>[0] = {}) {
  return roomMessage({ id, run_id: runID, kind: 'steer_request', body, created_at: `2026-08-14T10:00:0${id}Z`, ...over })
}

function mount(draft: string) {
  useStore.setState({
    info: { ...serverInfo, member: alice },
    roomMessages: {
      [runID]: [
        sent('1', 'first'),
        sent('2', 'from someone else', { actor_id: 'mem_other' }),
        sent('3', 'a note', { kind: 'comment' }),
        sent('4', 'second\nwith two lines'),
        sent('5', 'third'),
      ],
    },
  })
  saveComposerDraft(runID, { body: draft })
  const Box = () => {
    const { body } = useComposerDraft(runID)
    const setBody = (next: string) => saveComposerDraft(runID, { body: next })
    const recall = usePromptHistory(runID, body, setBody)
    return <textarea aria-label="Message the agent" value={body} onChange={(e) => setBody(e.target.value)} onKeyDown={recall} />
  }
  render(<Box />)
  return screen.getByRole('textbox', { name: 'Message the agent' }) as HTMLTextAreaElement
}

const press = (box: HTMLTextAreaElement, key: string) => fireEvent.keyDown(box, { key })

afterEach(() => saveComposerDraft(runID, { body: '' }))

describe('usePromptHistory', () => {
  it('walks back through your own prompts with Up and forward to the draft with Down', () => {
    const box = mount('half typed')
    box.setSelectionRange(0, 0)

    press(box, 'ArrowUp')
    expect(box.value).toBe('third')
    press(box, 'ArrowUp')
    expect(box.value).toBe('second\nwith two lines')
    press(box, 'ArrowUp')
    expect(box.value).toBe('first')
    press(box, 'ArrowUp')
    expect(box.value).toBe('first')

    press(box, 'ArrowDown')
    expect(box.value).toBe('second\nwith two lines')
    press(box, 'ArrowDown')
    press(box, 'ArrowDown')
    expect(box.value).toBe('half typed')
    press(box, 'ArrowDown')
    expect(box.value).toBe('half typed')
  })

  it('leaves the arrows to the caret unless the box is empty or the caret is at the start', () => {
    const box = mount('a long paragraph that wraps')
    box.setSelectionRange(20, 20)
    press(box, 'ArrowUp')
    expect(box.value).toBe('a long paragraph that wraps')

    box.setSelectionRange(0, 0)
    press(box, 'ArrowUp')
    expect(box.value).toBe('third')
  })

  it('ends the walk on an edit even if the text is restored', () => {
    const box = mount('')
    press(box, 'ArrowUp')
    fireEvent.change(box, { target: { value: 'third, edited' } })
    press(box, 'ArrowDown')
    expect(box.value).toBe('third, edited')
    fireEvent.change(box, { target: { value: 'third' } })
    box.setSelectionRange(0, 0)
    press(box, 'ArrowUp')
    expect(box.value).toBe('third')
  })

  it('keeps walking the prompts it started with when a new one arrives', () => {
    const box = mount('')
    press(box, 'ArrowUp')
    act(() => {
      useStore.setState((s) => ({ roomMessages: { [runID]: [...s.roomMessages[runID]!, sent('6', 'newest')] } }))
    })
    press(box, 'ArrowUp')
    expect(box.value).toBe('second\nwith two lines')
  })
})
