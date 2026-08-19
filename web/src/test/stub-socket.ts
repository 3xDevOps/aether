/** A WebSocket stand-in: records what was sent, lets a test drive the events. */
export class StubSocket {
  static opened: StubSocket[] = []

  onopen: (() => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((ev: { code: number; reason?: string }) => void) | null = null
  readonly sent: string[] = []
  closed = false

  constructor(readonly url: string) {
    StubSocket.opened.push(this)
  }

  send(data: string): void {
    this.sent.push(data)
  }

  close(): void {
    this.closed = true
  }

  frames(): unknown[] {
    return this.sent.map((f) => JSON.parse(f))
  }

  static install(): void {
    StubSocket.opened = []
    vi.stubGlobal('WebSocket', StubSocket)
  }

  static last(): StubSocket {
    return StubSocket.opened[StubSocket.opened.length - 1]
  }
}
