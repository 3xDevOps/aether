// The /ws/envscan client: one JSON start frame, then output and status
// frames until exactly one terminal outcome - a result, an error, or an
// unexpected close. Closing the session cancels the scan and reports
// nothing.

import { api, type EnvScanSession } from '@/lib/api'
import type { EnvScanRequest, EnvScanResult, EnvScanStatus } from '@/lib/types'
import { StubSocket } from '@/test/stub-socket'

let outputs: string[] = []
let statuses: EnvScanStatus[] = []
let results: EnvScanResult[] = []
let errors: { detail: string; tail?: string }[] = []

const inventoryReq: EnvScanRequest = { harness: 'claude', mode: 'inventory' }

function open(req: EnvScanRequest = inventoryReq): EnvScanSession {
  outputs = []
  statuses = []
  results = []
  errors = []
  return api.openEnvScan(req, {
    onOutput: (line) => outputs.push(line),
    onStatus: (status) => statuses.push(status),
    onResult: (result) => results.push(result),
    onError: (detail, tail) => errors.push({ detail, tail }),
  })
}

function frame(data: object) {
  StubSocket.last().onmessage?.({ data: JSON.stringify(data) })
}

beforeEach(() => {
  StubSocket.install()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('openEnvScan', () => {
  it('opens /ws/envscan and sends a bare start frame for an inventory scan', () => {
    const s = open()
    StubSocket.last().onopen?.()

    expect(StubSocket.last().url).toContain('/ws/envscan')
    expect(StubSocket.last().frames()[0]).toEqual({
      harness: 'claude',
      mode: 'inventory',
    })
    s.close()
  })

  it('carries the repository folder on a repo scan', () => {
    const s = open({
      harness: 'claude',
      mode: 'repo',
      repo_path: '/home/alice/code/myproject',
    })
    StubSocket.last().onopen?.()

    expect(StubSocket.last().frames()[0]).toEqual({
      harness: 'claude',
      mode: 'repo',
      repo_path: '/home/alice/code/myproject',
    })
    s.close()
  })

  it('carries the previous pair and feedback on a refine scan', () => {
    const s = open({
      harness: 'codex',
      mode: 'refine',
      previous_dockerfile: 'FROM ubuntu:24.04\n',
      previous_manifest_json: '[]',
      feedback: 'drop the rust toolchain',
    })
    StubSocket.last().onopen?.()

    expect(StubSocket.last().frames()[0]).toEqual({
      harness: 'codex',
      mode: 'refine',
      previous_dockerfile: 'FROM ubuntu:24.04\n',
      previous_manifest_json: '[]',
      feedback: 'drop the rust toolchain',
    })
    s.close()
  })

  it('routes output and status frames to their handlers', () => {
    const s = open()
    StubSocket.last().onopen?.()
    frame({ type: 'status', status: 'running' })
    frame({ type: 'output', line: 'inspecting node' })
    frame({ type: 'status', status: 'validating' })

    expect(statuses).toEqual(['running', 'validating'])
    expect(outputs).toEqual(['inspecting node'])
    expect(results).toEqual([])
    expect(errors).toEqual([])
    s.close()
  })

  it('settles on the result frame; the following close reports nothing', () => {
    const s = open()
    StubSocket.last().onopen?.()
    frame({
      type: 'result',
      dockerfile: 'FROM ubuntu:24.04\n',
      manifest: [
        {
          name: 'jq',
          version: '1.7.1',
          start_line: 1,
          end_line: 1,
          check_command: 'jq --version',
        },
      ],
    })
    StubSocket.last().onclose?.({ code: 1000 })

    expect(results).toHaveLength(1)
    expect(results[0].dockerfile).toBe('FROM ubuntu:24.04\n')
    expect(results[0].manifest[0].name).toBe('jq')
    expect(errors).toEqual([])
    s.close()
  })

  it('settles on the error frame with the detail and output tail', () => {
    const s = open()
    StubSocket.last().onopen?.()
    frame({ type: 'error', detail: 'the scan timed out after 10m0s', output_tail: 'last line' })
    StubSocket.last().onclose?.({ code: 1000 })

    expect(errors).toEqual([{ detail: 'the scan timed out after 10m0s', tail: 'last line' }])
    expect(results).toEqual([])
    s.close()
  })

  it('reports an unexpected close as an error', () => {
    const s = open()
    StubSocket.last().onopen?.()
    StubSocket.last().onclose?.({ code: 1006 })

    expect(errors).toEqual([{ detail: 'connection closed (1006)', tail: undefined }])
    s.close()
  })

  it('close cancels the scan and reports nothing', () => {
    const s = open()
    StubSocket.last().onopen?.()
    s.close()
    StubSocket.last().onclose?.({ code: 1000 })

    expect(StubSocket.last().closed).toBe(true)
    expect(results).toEqual([])
    expect(errors).toEqual([])
  })
})
