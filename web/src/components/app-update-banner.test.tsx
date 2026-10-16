import { fireEvent, render, screen } from '@testing-library/react'
import type { AetherAndroid } from '@/components/app-update-banner'
import { UpdateCenter } from '@/components/update-banner'
import type { UpdateStatus } from '@/lib/types'
import { useStore } from '@/store'
import { fakeApi, updateStatus } from '@/test/fixtures'
import { caps, seed, settle } from '@/test/update-banner-harness'

const phone = window as Window & { aetherAndroid?: AetherAndroid }

/** What the Android app's sidecar answers: the release, nothing to install. */
function packagedStatus(over: Partial<UpdateStatus['cli']> = {}): UpdateStatus {
  const status = updateStatus({ supervised: false, cli_path: undefined, install_method: undefined })
  return {
    ...status,
    cli: { ...status.cli, packaged: 'Android app', asset: undefined, can_self_update: false, ...over },
  }
}

function bridge(source: string, opened = ''): AetherAndroid {
  return { updateSource: vi.fn(() => source), openUpdate: vi.fn(() => opened) }
}

async function show(status: UpdateStatus) {
  const client = fakeApi({ localUpdateCheck: vi.fn().mockResolvedValue(status) })
  seed({ capabilities: caps({ local: ['link.status', 'update.check', 'update.apply'] }) })
  render(<UpdateCenter client={client} />)
  await settle()
  return client
}

beforeEach(() => {
  vi.useFakeTimers()
  useStore.setState({ updatesOpen: true })
  delete phone.aetherAndroid
})

afterEach(() => {
  vi.useRealTimers()
  delete phone.aetherAndroid
})

describe('the Android app update notice', () => {
  test('a Play install opens Google Play and never installs anything itself', async () => {
    phone.aetherAndroid = bridge('play')
    const client = await show(packagedStatus())

    expect(screen.getByText('Aether v1.3.0 is available.')).toBeTruthy()
    expect(screen.getByText('You are running v1.2.3.')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Release notes' }).getAttribute('href')).toBe(
      'https://github.com/3xDevOps/Aether/releases/tag/v1.3.0',
    )
    expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Open Google Play' }))
    expect(phone.aetherAndroid.openUpdate).toHaveBeenCalledWith('v1.3.0')
    expect(client.localUpdateApply).not.toHaveBeenCalled()
  })

  test('a release install opens the release page', async () => {
    phone.aetherAndroid = bridge('release')
    await show(packagedStatus())

    expect(screen.getByText(/Download aether-android\.apk from the release page/)).toBeTruthy()
    // The button opens the release page, so a second link to it would be noise.
    expect(screen.queryByRole('link', { name: 'Release notes' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Open release page' }))
    expect(phone.aetherAndroid.openUpdate).toHaveBeenCalledWith('v1.3.0')
  })

  test('a page that would not open shows the shell’s own error', async () => {
    const why = 'could not open market://details?id=io.aether.android: No Activity found to handle Intent'
    phone.aetherAndroid = bridge('play', why)
    await show(packagedStatus())

    fireEvent.click(screen.getByRole('button', { name: 'Open Google Play' }))
    expect(screen.getByText(why)).toBeTruthy()
  })

  test('a dismissed release stays quiet until the next one', async () => {
    phone.aetherAndroid = bridge('play')
    await show(packagedStatus())
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))

    expect(screen.queryByText('Aether v1.3.0 is available.')).toBeNull()
    expect(useStore.getState().dismissedUpdates.cli).toBe('v1.3.0')
  })

  test('nothing shows while the app is current', async () => {
    phone.aetherAndroid = bridge('play')
    await show(packagedStatus({ latest: 'v1.2.3', update_available: false }))

    expect(screen.queryByText(/is available/)).toBeNull()
    expect(screen.getByText('Updates come from the Android app: install its new version to update Aether.')).toBeTruthy()
  })
})
