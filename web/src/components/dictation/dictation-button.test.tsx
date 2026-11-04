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

    fireEvent.click(screen.getByRole('button', { name: 'Start dictation' }))
    const stop = await screen.findByRole('button', { name: 'Stop dictation' })
    expect(stop.getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('status').textContent).toMatch(/^Recording/)
    expect(getUserMedia).toHaveBeenCalledTimes(1)
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
    expect(screen.queryByRole('button', { name: 'Start dictation' })).toBeNull()
  })

  it('shows the server error as it came', async () => {
    const refusal = 'dictation.transcribe: dictation is busy: 3 requests queued; try again'
    vi.mocked(api.dictationTranscribe).mockRejectedValue(new Error(refusal))
    const box = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Start dictation' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Stop dictation' }))

    expect((await screen.findByRole('dialog')).textContent).toContain(refusal)
    expect(screen.getByRole('status').textContent).toBe(refusal)
    expect(box.value).toBe('fix the bug')
  })

  it('names a refused microphone by the browser error', async () => {
    getUserMedia.mockRejectedValueOnce(new DOMException('Permission denied', 'NotAllowedError'))
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'Start dictation' }))
    expect((await screen.findByRole('dialog')).textContent).toContain('Microphone: NotAllowedError: Permission denied')
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
    fireEvent.click(screen.getByRole('button', { name: 'Start dictation' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Stop dictation' }))
    await waitFor(() => expect((screen.getByRole('textbox', { name: 'Task' }) as HTMLTextAreaElement).value).toBe('add tests'))
    expect(submit).not.toHaveBeenCalled()
  })

  it('drops the recording on Escape', async () => {
    const box = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Start dictation' }))
    await screen.findByRole('button', { name: 'Stop dictation' })
    act(() => {
      fireEvent.keyDown(box, { key: 'Escape' })
    })
    expect(track.stop).toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Start dictation' })).toBeTruthy()
    expect(api.dictationTranscribe).not.toHaveBeenCalled()
  })
})

describe('insertTranscript', () => {
  it('spaces the text from the words around the caret and replaces a selection', () => {
    expect(insertTranscript('fix the bug', 'now', 11, 11)).toEqual({ body: 'fix the bug now', caret: 15 })
    expect(insertTranscript('fix the bug', 'a', 4, 7)).toEqual({ body: 'fix a bug', caret: 5 })
    expect(insertTranscript('', 'hello', 0, 0)).toEqual({ body: 'hello', caret: 5 })
  })
})
