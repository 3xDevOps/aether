import { type ReactNode, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Code } from '@/components/ui/code'
import type { Api } from '@/lib/api'
import type { SlackLinkResult, SlackStatusResult } from '@/lib/types'
import { errorSentence } from '@/lib/format'
import { disablePush, enablePush, readPush, testPush, type PushState } from '@/lib/push'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'

const docs = 'https://github.com/3xDevOps/Aether/blob/main/docs'

function Guide({ page = 'networking', to, children }: { page?: string; to?: string; children: ReactNode }) {
  return (
    <a href={`${docs}/${page}.md${to ? `#${to}` : ''}`} target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">
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

function SlackRow({ client }: { client: Api }) {
  const [status, setStatus] = useState<SlackStatusResult | null>(null)
  const [code, setCode] = useState<SlackLinkResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const read = () => client.slackStatus().then(setStatus)
  useEffect(() => {
    let live = true
    client.slackStatus().then(
      (next) => live && setStatus(next),
      (err: unknown) => live && setError(errorSentence(err)),
    )
    return () => {
      live = false
    }
  }, [client])

  const act = (work: Promise<unknown>) => {
    setBusy(true)
    setError(null)
    work.catch((err: unknown) => setError(errorSentence(err))).finally(() => setBusy(false))
  }
  const link = () => act(client.slackLink().then(setCode))
  const unlink = () => act(client.slackUnlink().then(() => {
    setCode(null)
    return read()
  }))

  const bot = code?.bot || status?.bot || 'the Aether bot'
  const team = code?.team || status?.team
  let help: ReactNode = error ? null : 'Checking Slack…'
  let control: ReactNode
  if (status && !status.configured) {
    help = <>This server has no Slack bot. An admin sets one up with <Code>sudo aether-server slack setup</Code>. <Guide page="slack">Slack guide</Guide></>
  } else if (status?.link) {
    help = `Linked to Slack user ${status.link.user_id}. Runs that need you arrive as direct messages from ${bot}.`
    control = <Button size="sm" variant="secondary" disabled={busy} onClick={unlink}>Unlink</Button>
  } else if (status) {
    help = 'Get a direct message in Slack when a run needs you, and answer it there.'
    control = (
      <div className="flex gap-2">
        {code && <Button size="sm" variant="secondary" disabled={busy} onClick={() => act(read())}>Check again</Button>}
        <Button size="sm" variant="secondary" disabled={busy} onClick={link}>{code ? 'New code' : 'Link Slack'}</Button>
      </div>
    )
  }

  return (
    <SettingRow label="Slack" help={help} control={control}>
      {status?.configured && !status.connected && status.error && (
        <Callout tone="failed">The server is not connected to Slack: {status.error}</Callout>
      )}
      {code && !status?.link && (
        <p className="text-ui-sm text-muted">
          Send this code to {bot} in Slack{team ? ` (${team})` : ''} within 10 minutes: <Code>{code.code}</Code>
        </p>
      )}
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
    </SettingRow>
  )
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
      <SlackRow client={client} />
    </SettingsSection>
  )
}
