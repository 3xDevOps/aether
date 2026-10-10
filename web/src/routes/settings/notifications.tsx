import { type ReactNode, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Code } from '@/components/ui/code'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { disablePush, enablePush, readPush, testPush, type PushState } from '@/lib/push'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'

const guide = 'https://github.com/3xDevOps/Aether/blob/main/docs/networking.md'

function Guide({ to, children }: { to: string; children: ReactNode }) {
  return (
    <a href={`${guide}#${to}`} target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">
      {children}
    </a>
  )
}

const hosted = <Guide to="the-dashboard">the dashboard your server hosts</Guide>

function help(state: PushState | null): ReactNode {
  if (!state) return 'Checking this device…'
  if (state.kind === 'blocked') {
    return 'This browser blocks notifications from this dashboard. Allow them in its site settings, then reload.'
  }
  if (state.kind !== 'unsupported') {
    return 'When one of your runs asks for a permission or an answer, waits for your reply, or reports a result to review. Tapping the notification opens the run.'
  }
  switch (state.why) {
    case 'desktop':
      return <>The desktop app notifies you itself while it is open. To be notified on another device, open {hosted} there and turn this on.</>
    case 'gateway':
      return <><Code>aether gui</Code> serves this dashboard from your computer, where a notification could not open it later. Open {hosted} on the device and turn this on there.</>
    case 'home-screen':
      return <>On iPhone and iPad, notifications reach Aether only when it is opened from the home screen. <Guide to="add-it-to-your-home-screen">Add it to your home screen</Guide>, open it from there and turn this on.</>
    case 'browser':
      return <>This browser cannot receive push notifications. On a phone, open this dashboard in Chrome or Safari and <Guide to="add-it-to-your-home-screen">add it to your home screen</Guide>.</>
  }
}

export function NotificationsSection({ client }: { client: Api }) {
  const gateway = useStore((s) => s.capabilities?.gateway)
  const [state, setState] = useState<PushState | null>(null)
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    readPush(client, gateway).then(
      (next) => live && setState(next),
      (err: unknown) => {
        if (!live) return
        setState({ kind: 'off' })
        setError(errorSentence(err))
      },
    )
    return () => {
      live = false
    }
  }, [client, gateway])

  const act = (work: Promise<PushState>, done = false) => {
    setBusy(true)
    setSent(false)
    setError(null)
    work
      .then((next) => {
        setState(next)
        setSent(done)
      }, (err: unknown) => setError(errorSentence(err)))
      .finally(() => setBusy(false))
  }
  const test = () => act(
    testPush(client).then(
      (): PushState => ({ kind: 'on' }),
      // The server forgets a device the push service reports gone.
      async (err: unknown) => {
        setState(await readPush(client, gateway))
        throw err
      },
    ),
    true,
  )

  const on = state?.kind === 'on'
  return (
    <SettingsSection title="Notifications">
      <SettingRow
        label="Notify this device when a run needs me"
        labelFor="settings-push"
        help={help(state)}
        control={(
          <Checkbox
            id="settings-push"
            checked={on}
            disabled={busy || (state?.kind !== 'on' && state?.kind !== 'off')}
            onCheckedChange={(checked) => act(checked === true ? enablePush(client) : disablePush(client))}
          />
        )}
      >
        {on && (
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
            <Button size="sm" variant="secondary" disabled={busy} onClick={test}>Send test notification</Button>
            {sent && <p role="status" className="text-ui-sm text-muted">Sent. It should appear on this device in a few seconds.</p>}
          </div>
        )}
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
      </SettingRow>
    </SettingsSection>
  )
}
