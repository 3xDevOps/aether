import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useRef } from 'react'
import { AssistantComposer } from '@/components/assistant/composer'
import { DictationButton } from '@/components/dictation/dictation-button'
import { insertTranscript } from '@/components/dictation/text'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { alice, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

const hardwareRate = 48_000
const track = { stop: vi.fn() }
const getUserMedia = vi.fn(async () => ({ getTracks: () => [track] }))

class FakeWorklet {
  port = {
    onmessage: null as ((event: { data: unknown }) => void) | null,
    // One second of a quiet tone, then the end of the recording.
    postMessage: () => {
      this.port.onmessage?.({ data: new Float32Array(hardwareRate).fill(0.25) })
      this.port.onmessage?.({ data: 'done' })
    },
  }
  connect() {}
}

class FakeContext {
  sampleRate = hardwareRate
  destination = {}
  audioWorklet = { addModule: async () => {} }
  createMediaStreamSource = () => ({ connect() {} })
  createAnalyser = () => ({ fftSize: 0, getFloatTimeDomainData: (frame: Float32Array) => frame.fill(0.2) })
  resume = async () => {}
  close = async () => {}
}

beforeEach(() => {
  track.stop.mockClear()
  getUserMedia.mockClear()
  vi.mocked(api.dictationTranscribe).mockReset()
  vi.stubGlobal('AudioContext', FakeContext)
  vi.stubGlobal('AudioWorkletNode', FakeWorklet)
  Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: { getUserMedia } })
  URL.createObjectURL = () => 'blob:recorder'
  URL.revokeObjectURL = () => {}
  useStore.setState({
    workspaces: { [workspace.id]: { ...workspace, name: 'aether' } },
    activeWorkspace: workspace.id,
    capabilities: { gateway: 'local', methods: ['*'], ws: [] },
    info: { ...serverInfo, member: alice, features: ['dictation'] },
    assistantDraft: 'fix the bug',
  })
})

afterEach(() => vi.unstubAllGlobals())

function mount() {
  const Composer = () => {
    const textarea = useRef<HTMLTextAreaElement>(null)
    return <AssistantComposer textarea={textarea} running={false} />
  }
  render(<Composer />)
  return screen.getByRole('textbox', { name: 'Message the assistant' }) as HTMLTextAreaElement
}

function wav(base64: string): DataView {
  return new DataView(Uint8Array.from(atob(base64), (c) => c.charCodeAt(0)).buffer)
}

describe('DictationButton', () => {
  it('records, sends a 16 kHz mono WAV and puts the text at the caret without sending', async () => {
    vi.mocked(api.dictationTranscribe).mockResolvedValue({ text: ' hello world ' })
    const box = mount()
    expect(getUserMedia).not.toHaveBeenCalled()
    box.setSelectionRange(4, 4)

    fireEvent.click(screen.getByRole('button', { name: 'Dictate', pressed: false }))
    const stop = await screen.findByRole('button', { name: 'Dictate', pressed: true })
    expect(screen.getByRole('status').textContent).toMatch(/^Recording/)
    expect(getUserMedia).toHaveBeenCalledTimes(1)
    const send = screen.getByRole('button', { name: /^Send/ }) as HTMLButtonElement
    expect(send.disabled).toBe(true)
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.click(stop)

    await waitFor(() => expect(box.value).toBe('fix hello world the bug'))
    expect(track.stop).toHaveBeenCalled()
    const [audio, hints] = vi.mocked(api.dictationTranscribe).mock.calls[0]
    const header = wav(audio)
    expect(String.fromCharCode(...new Uint8Array(header.buffer, 0, 4))).toBe('RIFF')
    expect(header.getUint16(22, true)).toBe(1)
    expect(header.getUint32(24, true)).toBe(16_000)
    expect(header.getUint16(34, true)).toBe(16)
    expect(header.getUint32(40, true)).toBe(16_000 * 2)
    expect(hints).toContain('aether')
    await waitFor(() => expect(document.activeElement).toBe(box))
    expect(box.selectionStart).toBe('fix hello world'.length)
    expect(api.assistantPrompt).not.toHaveBeenCalled()
  })

  it('shows nothing while the server lacks the dictation feature', () => {
    useStore.setState({ info: { ...serverInfo, member: alice } })
    mount()
    expect(screen.queryByRole('button', { name: 'Dictate' })).toBeNull()
  })

  it('shows the server error as it came', async () => {
    const refusal = 'dictation.transcribe: dictation is busy: 3 requests queued; try again'
    vi.mocked(api.dictationTranscribe).mockRejectedValue(new Error(refusal))
    const box = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Dictate', pressed: false }))
    fireEvent.click(await screen.findByRole('button', { name: 'Dictate', pressed: true }))

    expect((await screen.findByRole('alert')).textContent).toBe(refusal)
    expect(box.value).toBe('fix the bug')
  })

  it('names a refused microphone by the browser error', async () => {
    getUserMedia.mockRejectedValueOnce(new DOMException('Permission denied', 'NotAllowedError'))
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'Dictate', pressed: false }))
    expect((await screen.findByRole('alert')).textContent).toContain('Microphone: NotAllowedError: Permission denied')
  })

  it('never submits the form it sits in', async () => {
    vi.mocked(api.dictationTranscribe).mockResolvedValue({ text: 'add tests' })
    const submit = vi.fn((event: React.FormEvent) => event.preventDefault())
    const Form = () => {
      const textarea = useRef<HTMLTextAreaElement>(null)
      return (
        <form onSubmit={submit}>
          <textarea aria-label="Task" ref={textarea} defaultValue="" />
          <DictationButton textarea={textarea} onInsert={(next) => { textarea.current!.value = next }} />
        </form>
      )
    }
    render(<Form />)
    fireEvent.click(screen.getByRole('button', { name: 'Dictate', pressed: false }))
    fireEvent.click(await screen.findByRole('button', { name: 'Dictate', pressed: true }))
    await waitFor(() => expect((screen.getByRole('textbox', { name: 'Task' }) as HTMLTextAreaElement).value).toBe('add tests'))
    expect(submit).not.toHaveBeenCalled()
  })

  it('follows the caret the member left when they edit the box while recording', async () => {
    vi.mocked(api.dictationTranscribe).mockResolvedValue({ text: 'quickly' })
    const box = mount()
    box.setSelectionRange(4, 4)
    fireEvent.click(screen.getByRole('button', { name: 'Dictate', pressed: false }))
    const stop = await screen.findByRole('button', { name: 'Dictate', pressed: true })
    fireEvent.change(box, { target: { value: 'please fix the bug' } })
    box.focus()
    box.setSelectionRange(18, 18)
    fireEvent.click(stop)
    await waitFor(() => expect(box.value).toBe('please fix the bug quickly'))
  })

  it('stops from the keyboard after a tap released while the mic was starting', async () => {
    vi.mocked(api.dictationTranscribe).mockResolvedValue({ text: 'done' })
    const box = mount()
    box.setSelectionRange(11, 11)
    const mic = screen.getByRole('button', { name: 'Dictate', pressed: false })
    fireEvent.pointerDown(mic, { button: 0, pointerId: 1 })
    fireEvent.pointerUp(mic, { button: 0, pointerId: 1 })
    const stop = await screen.findByRole('button', { name: 'Dictate', pressed: true })
    fireEvent.click(stop, { detail: 0 })
    await waitFor(() => expect(box.value).toBe('fix the bug done'))
  })

  it('leaves an Escape pressed elsewhere to its owner', async () => {
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'Dictate', pressed: false }))
    await screen.findByRole('button', { name: 'Dictate', pressed: true })
    act(() => {
      fireEvent.keyDown(document.body, { key: 'Escape' })
    })
    expect(track.stop).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Dictate', pressed: true })).toBeTruthy()
  })

  it('drops the recording on Escape', async () => {
    const box = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Dictate', pressed: false }))
    await screen.findByRole('button', { name: 'Dictate', pressed: true })
    act(() => {
      fireEvent.keyDown(box, { key: 'Escape' })
    })
    expect(track.stop).toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Dictate', pressed: false })).toBeTruthy()
    expect(api.dictationTranscribe).not.toHaveBeenCalled()
  })
})

describe('insertTranscript', () => {
  it('spaces the text from the words around the caret and replaces a selection', () => {
    expect(insertTranscript('fix the bug', 'now', 11, 11)).toEqual({ body: 'fix the bug now', caret: 15 })
    expect(insertTranscript('fix the bug', 'a', 4, 7)).toEqual({ body: 'fix a bug', caret: 5 })
    expect(insertTranscript('', 'hello', 0, 0)).toEqual({ body: 'hello', caret: 5 })
  })

  it('keeps punctuation and brackets attached', () => {
    expect(insertTranscript('keep the old.', 'one', 12, 12)).toEqual({ body: 'keep the old one.', caret: 16 })
    expect(insertTranscript('Done.', 'next', 5, 5)).toEqual({ body: 'Done. next', caret: 10 })
    expect(insertTranscript('call ()', 'it', 6, 6)).toEqual({ body: 'call (it)', caret: 8 })
    expect(insertTranscript('a, b', 'and', 1, 1)).toEqual({ body: 'a and, b', caret: 5 })
  })
})
