// The gateway's HTTP surface, for the setup a scenario is not about. A test
// that walks the wizard as a second member still needs a first member to
// have created the workspace; doing that through the same real gateway keeps
// the fixture honest without spending a browser on it.

/** One call's failure, carrying the gateway's own error text. */
export class GatewayError extends Error {
  constructor(
    readonly status: number,
    readonly path: string,
    detail: string,
  ) {
    super(`${path}: ${status}: ${detail}`)
    this.name = 'GatewayError'
  }
}

export class GatewayClient {
  constructor(
    private readonly addr: string,
    private readonly token: string,
  ) {}

  /** A control-channel method, proxied to the server over SSH. */
  rpc<T>(method: string, params: unknown = {}): Promise<T> {
    return this.post<T>(`/api/v1/${method}`, params)
  }

  /** A client-machine verb, answered by the gateway itself. */
  local<T>(verb: string, params: unknown = {}): Promise<T> {
    return this.post<T>(`/local/v1/${verb}`, params)
  }

  private async post<T>(path: string, params: unknown): Promise<T> {
    const res = await fetch(`http://${this.addr}${path}`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        authorization: `Bearer ${this.token}`,
      },
      body: JSON.stringify(params),
      // A gateway that never answers must fail the call, not hang the test.
      // Teardown asks it what to clean up, and that is the last thing that
      // should be able to wedge a run.
      signal: AbortSignal.timeout(60_000),
    })
    const body = await res.text()
    if (!res.ok) throw new GatewayError(res.status, path, body)
    return JSON.parse(body) as T
  }
}

export interface WorkspaceResult {
  workspace: { id: string }
}

export interface InviteResult {
  code: string
}

export interface RepoPushResult {
  state: 'pushed' | 'up-to-date' | 'behind' | 'diverged'
  local_commit: string
  output: string
}
