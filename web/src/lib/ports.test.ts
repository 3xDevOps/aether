import { afterEach, describe, expect, it, vi } from 'vitest'
import { forwardCommand, openOnThisComputer, portAddress, runOnly } from '@/lib/ports'
import type { DevPort } from '@/lib/types'
import { fakeApi } from '@/test/fixtures'

const everywhere: DevPort = { port: 8000, address: '0.0.0.0', process: 'python3.12' }
const loopback: DevPort = { port: 5173, address: '::1', process: 'node' }

afterEach(() => vi.restoreAllMocks())

describe('a run port', () => {
  it('is addressed as localhost unless it is bound to one other address', () => {
    expect(portAddress(everywhere)).toBe('localhost:8000')
    expect(portAddress({ port: 3000, address: '::' })).toBe('localhost:3000')
    expect(portAddress(loopback)).toBe('localhost:5173')
    expect(portAddress({ port: 4000, address: '127.0.0.2' })).toBe('127.0.0.2:4000')
    expect(portAddress({ port: 9000, address: '172.17.0.6' })).toBe('172.17.0.6:9000')
    expect(portAddress({ port: 9000, address: 'fd00::6' })).toBe('[fd00::6]:9000')
  })

  it('is the run’s alone when bound to loopback', () => {
    expect(runOnly(loopback)).toBe(true)
    expect(runOnly({ port: 1455, address: '127.0.0.1' })).toBe(true)
    expect(runOnly(everywhere)).toBe(false)
  })

  it('names the command that forwards it', () => {
    expect(forwardCommand('run-abc', 8000)).toBe('aether forward run-abc 8000')
  })

})

describe('opening a run port on this computer', () => {
  const tab = () => ({ opener: {} as unknown, close: vi.fn(), location: { replace: vi.fn() } })

  it('forwards the port and loads it in the tab opened by the click', async () => {
    const opened = tab()
    const open = vi.spyOn(window, 'open').mockReturnValue(opened as unknown as Window)
    const client = fakeApi({ devPortList: vi.fn(async () => ({ ports: [everywhere] })) })

    await expect(openOnThisComputer(client, 'run-abc', 8000)).resolves.toBe('http://localhost:8000')

    expect(open).toHaveBeenCalledExactlyOnceWith('about:blank', '_blank')
    expect(opened.opener).toBeNull()
    expect(client.localForwardStart).toHaveBeenCalledWith('run:run-abc', 8000)
    expect(opened.location.replace).toHaveBeenCalledWith('http://localhost:8000')
  })

  it('opens the address afterwards where the first tab was refused', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const client = fakeApi({ devPortList: vi.fn(async () => ({ ports: [everywhere] })) })

    await openOnThisComputer(client, 'run-abc', 8000)

    expect(open).toHaveBeenLastCalledWith('http://localhost:8000', '_blank', 'noopener,noreferrer')
  })

  it('closes the tab and forwards nothing for a port that stopped listening', async () => {
    const opened = tab()
    vi.spyOn(window, 'open').mockReturnValue(opened as unknown as Window)
    const client = fakeApi()

    await expect(openOnThisComputer(client, 'run-abc', 8000)).rejects.toThrow('Nothing in the session is listening on port 8000.')

    expect(client.localForwardStart).not.toHaveBeenCalled()
    expect(opened.close).toHaveBeenCalled()
  })

  it('forwards nothing for a port only the run can reach, and says which address it is bound to', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    const client = fakeApi({ devPortList: vi.fn(async () => ({ ports: [loopback] })) })

    await expect(openOnThisComputer(client, 'run-abc', 5173)).rejects.toThrow('Port 5173 is bound to ::1 inside the session, so only the session can reach it. Start the server on 0.0.0.0 to open it on this computer.')

    expect(client.localForwardStart).not.toHaveBeenCalled()
  })

  it('closes the tab and keeps the gateway’s refusal', async () => {
    const opened = tab()
    vi.spyOn(window, 'open').mockReturnValue(opened as unknown as Window)
    const client = fakeApi({
      devPortList: vi.fn(async () => ({ ports: [everywhere] })),
      localForwardStart: vi.fn(async () => { throw new Error('localops: listen on 127.0.0.1:8000: bind: address already in use') }),
    })

    await expect(openOnThisComputer(client, 'run-abc', 8000)).rejects.toThrow('localops: listen on 127.0.0.1:8000: bind: address already in use')

    expect(opened.close).toHaveBeenCalled()
    expect(opened.location.replace).not.toHaveBeenCalled()
  })
})
