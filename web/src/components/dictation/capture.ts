import { maxSeconds } from '@/components/dictation/wav'

const flushTimeout = 2_000

export interface Capture {
  /** Input loudness from 0 to 1, read from an AnalyserNode. */
  level: () => number
  /** Releases the microphone and returns the mono samples at `rate`. */
  stop: () => Promise<{ samples: Float32Array; rate: number }>
  /** Releases the microphone and drops the audio. */
  cancel: () => void
}

// Downmixes each render quantum to mono and posts it in 4096-sample chunks; "flush" posts the rest, then "done".
const recorder = `
class Recorder extends AudioWorkletProcessor {
  constructor() {
    super()
    this.chunk = new Float32Array(4096)
    this.filled = 0
    this.port.onmessage = () => {
      this.port.postMessage(this.chunk.slice(0, this.filled))
      this.port.postMessage('done')
    }
  }
  process(inputs) {
    const channels = inputs[0]
    if (!channels || channels.length === 0) return true
    for (let i = 0; i < channels[0].length; i++) {
      let sum = 0
      for (const channel of channels) sum += channel[i]
      this.chunk[this.filled++] = sum / channels.length
      if (this.filled === this.chunk.length) {
        this.port.postMessage(this.chunk.slice())
        this.filled = 0
      }
    }
    return true
  }
}
registerProcessor('aether-dictation', Recorder)
`

/** Opens the microphone at the hardware rate: a 16 kHz AudioContext records silence on some macOS devices. */
export async function startCapture(): Promise<Capture> {
  if (!navigator.mediaDevices?.getUserMedia) {
    throw new Error(`The browser offers no microphone on ${location.protocol}//${location.host}: dictation needs an https page or localhost.`)
  }
  const stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true } })
  let context: AudioContext | undefined
  const release = () => {
    for (const track of stream.getTracks()) track.stop()
    void context?.close()
  }
  try {
    const audio = new AudioContext()
    context = audio
    const url = URL.createObjectURL(new Blob([recorder], { type: 'text/javascript' }))
    try {
      await audio.audioWorklet.addModule(url)
    } finally {
      URL.revokeObjectURL(url)
    }
    const source = audio.createMediaStreamSource(stream)
    const node = new AudioWorkletNode(audio, 'aether-dictation')
    const analyser = audio.createAnalyser()
    analyser.fftSize = 1024
    const frame = new Float32Array(analyser.fftSize)
    const limit = maxSeconds * audio.sampleRate
    const chunks: Float32Array[] = []
    let length = 0
    const done = new Promise<void>((resolve) => {
      node.port.onmessage = (event: MessageEvent<Float32Array | 'done'>) => {
        if (event.data === 'done') resolve()
        else if (length < limit) {
          chunks.push(event.data)
          length += event.data.length
        }
      }
    })
    source.connect(analyser)
    source.connect(node)
    // A worklet the graph never pulls is not processed; it writes no output, so this plays nothing.
    node.connect(audio.destination)
    await audio.resume()
    return {
      level: () => {
        analyser.getFloatTimeDomainData(frame)
        let sum = 0
        for (const sample of frame) sum += sample * sample
        const db = 10 * Math.log10(sum / frame.length || 1e-10)
        return Math.min(1, Math.max(0, (db + 60) / 50))
      },
      stop: async () => {
        for (const track of stream.getTracks()) track.stop()
        node.port.postMessage('flush')
        // A suspended or broken audio thread never answers; keep what already arrived.
        let timer = 0
        try {
          await Promise.race([done, new Promise<void>((resolve) => { timer = window.setTimeout(resolve, flushTimeout) })])
        } finally {
          clearTimeout(timer)
          void audio.close()
        }
        const samples = new Float32Array(Math.min(length, limit))
        let at = 0
        for (const chunk of chunks) {
          if (at >= samples.length) break
          samples.set(chunk.subarray(0, samples.length - at), at)
          at += chunk.length
        }
        return { samples, rate: audio.sampleRate }
      },
      cancel: release,
    }
  } catch (err) {
    release()
    throw err
  }
}
