import { describe, expect, it, vi } from 'vitest'
import type { LocalForwardStartResult } from '@/lib/api'
import {
  forwardOAuthCallback,
  oauthCallbackPort,
  openOAuthLink,
  remoteOAuthInstructions,
} from '@/lib/oauth-forward'
import { fakeApi } from '@/test/fixtures'

describe('OAuth callback forwarding', () => {
  it('extracts an encoded loopback redirect from an authorization URL', () => {
    const callback = 'http://localhost:1455/auth/callback'
    const uri = `https://auth.example/authorize?client_id=a&redirect_uri=${encodeURIComponent(callback)}`
    expect(oauthCallbackPort(uri)).toBe(1455)
  })

  it('accepts IPv4 and IPv6 loopback callbacks but rejects remote redirects', () => {
    expect(
      oauthCallbackPort(
        'https://auth.example/?callback_url=http%3A%2F%2F127.0.0.1%3A9876%2Foauth%2Fcallback',
      ),
    ).toBe(9876)
    expect(
      oauthCallbackPort(
        'https://auth.example/?return_uri=http%3A%2F%2F%5B%3A%3A1%5D%3A3210%2Fdone',
      ),
    ).toBe(3210)
    expect(
      oauthCallbackPort(
        'https://auth.example/?redirect_uri=https%3A%2F%2Fexample.com%2Fcallback',
      ),
    ).toBeNull()
  })

  it('does not treat unrelated localhost links as OAuth callbacks', () => {
    expect(oauthCallbackPort('http://localhost:3000/')).toBeNull()
    expect(oauthCallbackPort('not a URL')).toBeNull()
  })

  it('starts the matching target before reporting the callback port', async () => {
    const localForwardStart = vi.fn(async () => ({
      target: 'terminal',
      port: 1455,
      local_port: 1455,
      state: 'active' as const,
    }))
    const client = fakeApi({ localForwardStart })
    const uri =
      'https://auth.example/?redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback'

    await expect(forwardOAuthCallback(client, 'terminal', uri)).resolves.toBe(1455)
    expect(localForwardStart).toHaveBeenCalledWith('terminal', 1455)
  })

  it('holds the authorization page until the callback listener is ready', async () => {
    let release: (() => void) | undefined
    const localForwardStart = vi.fn(
      () =>
        new Promise<LocalForwardStartResult>((resolve) => {
          release = () =>
            resolve({
              target: 'terminal',
              port: 1455,
              local_port: 1455,
              state: 'active',
            })
        }),
    )
    const replace = vi.fn()
    const popup = { opener: window, location: { replace }, close: vi.fn() }
    const open = vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window)
    const ready = vi.fn()
    const failed = vi.fn()
    const uri =
      'https://auth.example/?redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback'

    expect(
      openOAuthLink(
        fakeApi({ localForwardStart }),
        'terminal',
        uri,
        ready,
        failed,
      ),
    ).toBe(true)
    expect(open).toHaveBeenCalledWith('about:blank', '_blank')
    expect(replace).not.toHaveBeenCalled()
    release?.()
    await vi.waitFor(() => expect(replace).toHaveBeenCalledWith(uri))
    expect(ready).toHaveBeenCalledWith(1455)
    expect(failed).not.toHaveBeenCalled()
  })

  it('names the forward command for a loopback callback and skips other links', () => {
    const uri =
      'https://auth.example/?redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback'

    expect(remoteOAuthInstructions('run:r1', uri)).toEqual({
      port: 1455,
      command: 'aether forward run:r1 1455',
    })
    expect(remoteOAuthInstructions('terminal', uri)).toEqual({
      port: 1455,
      command: 'aether forward terminal 1455',
    })
    expect(remoteOAuthInstructions('terminal', 'https://example.com/docs')).toBeNull()
  })

  it('leaves ordinary terminal links to the default opener', () => {
    expect(
      openOAuthLink(
        fakeApi(),
        'terminal',
        'https://example.com/docs',
        vi.fn(),
        vi.fn(),
      ),
    ).toBe(false)
  })
})
