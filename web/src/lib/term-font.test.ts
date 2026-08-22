import { terminalFontFamily, whenTerminalFontReady } from '@/lib/term-font'

// jsdom has no FontFaceSet; each case installs exactly the shape it needs.
function stubFonts(fonts: unknown) {
  Object.defineProperty(document, 'fonts', {
    value: fonts,
    configurable: true,
  })
}

// Async cases use fake timers throughout: advanceTimersByTimeAsync drains the
// microtask queue between ticks, so promise chains settle deterministically.
afterEach(() => {
  stubFonts(undefined)
  vi.useRealTimers()
})

describe('whenTerminalFontReady', () => {
  it('opens synchronously when the font API is missing', () => {
    stubFonts(undefined)
    let opened = false
    whenTerminalFontReady(() => {
      opened = true
    })
    expect(opened).toBe(true)
  })

  it('opens synchronously when both faces are already loaded', () => {
    stubFonts({ check: () => true, load: () => Promise.resolve([]) })
    let opened = false
    whenTerminalFontReady(() => {
      opened = true
    })
    expect(opened).toBe(true)
  })

  it('waits when only the regular face is loaded', async () => {
    vi.useFakeTimers()
    const loads: string[] = []
    stubFonts({
      check: (face: string) => !face.startsWith('bold'),
      load: (face: string) => {
        loads.push(face)
        return Promise.resolve([])
      },
    })
    let opened = false
    whenTerminalFontReady(() => {
      opened = true
    })
    expect(opened).toBe(false)
    await vi.advanceTimersByTimeAsync(0)
    expect(opened).toBe(true)
    expect(loads).toEqual(['12px "JetBrainsMono NFM"', 'bold 12px "JetBrainsMono NFM"'])
  })

  it('waits for the font load and then opens', async () => {
    vi.useFakeTimers()
    const { promise: loading, resolve } = Promise.withResolvers<void>()
    stubFonts({ check: () => false, load: () => loading })
    let opened = false
    whenTerminalFontReady(() => {
      opened = true
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(opened).toBe(false)
    resolve()
    await vi.advanceTimersByTimeAsync(0)
    expect(opened).toBe(true)
  })

  it('opens anyway when the font load rejects', async () => {
    vi.useFakeTimers()
    stubFonts({ check: () => false, load: () => Promise.reject(new Error('nope')) })
    let opened = false
    whenTerminalFontReady(() => {
      opened = true
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(opened).toBe(true)
  })

  it('opens after the timeout when the load hangs', async () => {
    vi.useFakeTimers()
    stubFonts({ check: () => false, load: () => new Promise(() => {}) })
    let opened = false
    whenTerminalFontReady(() => {
      opened = true
    })
    await vi.advanceTimersByTimeAsync(1999)
    expect(opened).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(opened).toBe(true)
  })

  it('never opens once cancelled', async () => {
    vi.useFakeTimers()
    const { promise: loading, resolve } = Promise.withResolvers<void>()
    stubFonts({ check: () => false, load: () => loading })
    let opened = false
    const cancel = whenTerminalFontReady(() => {
      opened = true
    })
    cancel()
    resolve()
    // Even the load settling and the timeout window expiring must not open.
    await vi.advanceTimersByTimeAsync(3000)
    expect(opened).toBe(false)
  })
})

describe('terminalFontFamily', () => {
  it('leads with the shipped Nerd Font and falls back to generic monospace', () => {
    const families = terminalFontFamily.split(',').map((f) => f.trim())
    expect(families[0]).toBe('"JetBrainsMono NFM"')
    expect(families[families.length - 1]).toBe('monospace')
  })
})
