import { type ReactNode, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Code } from '@/components/ui/code'
import { ApiError, codeUnavailable, type Api } from '@/lib/api'
import { desktopBridge } from '@/components/shell/window-bar'
import { Input } from '@/components/ui/input'
import { Radio, RadioGroup } from '@/components/ui/radio'
import type { NotifyLevel, NotifyPrefs, NotifyPrefsSetParams, SlackLinkResult, SlackStatusResult } from '@/lib/types'
import { errorSentence } from '@/lib/format'
import { disablePush, enablePush, readPush, testPush, type PushState } from '@/lib/push'
import { SettingRow, SettingsSection, TryAgain } from '@/routes/settings/layout'
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

function help(state: PushState | null, hinted = false): ReactNode {
  if (!state) return 'Reading this device…'
  if (state.kind === 'blocked') {
    return 'This browser blocks notifications from this dashboard. Allow them in its site settings, then reload.'
  }
  if (state.kind !== 'unsupported') {
    return 'When one of your sessions asks for a permission or an answer, waits for your reply, or reports a result to review. Tapping the notification opens the session.'
  }
  switch (state.why) {
    case 'desktop':
      return <>The desktop app notifies you itself while it is open. To be notified on another device, open {hosted} there and turn this on.</>
    case 'gateway':
      return <><Code>aether gui</Code> serves this dashboard from your computer, where a notification could not open it later. Open {hosted} on the device and turn this on there.</>
    case 'home-screen':
      if (hinted) return 'On iPhone and iPad, only the home-screen app gets notifications.'
      return <>On iPhone and iPad, notifications reach Aether only when it is opened from the home screen. <Guide to="add-it-to-your-home-screen">Add it to your home screen</Guide>, open it from there and turn this on.</>
    case 'browser':
      return <>This browser cannot receive push notifications. On a phone, open this dashboard in Chrome or Safari and <Guide to="add-it-to-your-home-screen">add it to your home screen</Guide>.</>
  }
}

export function SlackSection({ client }: { client: Api }) {
  const [status, setStatus] = useState<SlackStatusResult | null>(null)
  const [code, setCode] = useState<SlackLinkResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)

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
  }, [client, attempt])

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
  let help: ReactNode = error ? null : 'Reading Slack…'
  let control: ReactNode
  if (status && !status.configured) {
    help = <>This server has no Slack bot. An admin sets one up with <Code>sudo aether-server slack setup</Code>. <Guide page="slack">Slack guide</Guide></>
  } else if (status?.link) {
    help = `Linked to Slack user ${status.link.user_id}. Sessions that need you arrive as direct messages from ${bot}.`
    control = <Button size="sm" variant="secondary" disabled={busy} onClick={unlink}>Unlink</Button>
  } else if (status) {
    help = 'Get a direct message in Slack when a session needs you, and answer it there.'
    control = (
      <div className="flex gap-2">
        {code && <Button size="sm" variant="secondary" disabled={busy} onClick={() => act(read())}>Check again</Button>}
        <Button size="sm" variant="secondary" disabled={busy} onClick={link}>{code ? 'New code' : 'Link Slack'}</Button>
      </div>
    )
  }

  return (
    <SettingsSection title="Slack" id="slack" scope="Your account">
      <SettingRow label="Direct messages" help={help} control={control}>
        {status?.configured && !status.connected && status.error && (
          <Callout tone="failed">The server is not connected to Slack: {status.error}</Callout>
        )}
        {code && !status?.link && (
          <p className="text-ui-sm text-muted">
            Send this code to {bot} in Slack{team ? ` (${team})` : ''} within 10 minutes: <Code>{code.code}</Code>
          </p>
        )}
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        {error && !status && <TryAgain onClick={() => { setError(null); setAttempt((n) => n + 1) }} />}
      </SettingRow>
    </SettingsSection>
  )
}

export function NotificationSections({ client }: { client: Api }) {
  const [subscriptions, setSubscriptions] = useState(0)
  return (
    <>
      <PushSection client={client} onSubscriptionChange={() => setSubscriptions((n) => n + 1)} />
      <WatchNotifySection client={client} subscriptions={subscriptions} />
      <SlackSection client={client} />
    </>
  )
}

const installHintKey = 'aether.ios-install-hint.dismissed'

function hintDismissed(): boolean {
  try {
    return window.localStorage.getItem(installHintKey) === '1'
  } catch {
    return false
  }
}

function InstallHint({ onDismiss }: { onDismiss: () => void }) {
  return (
    <Callout
      tone="review"
      title="Install Aether to get notifications"
      actions={(
        <Button size="sm" variant="secondary" className="coarse:h-11" onClick={() => {
          try {
            window.localStorage.setItem(installHintKey, '1')
          } catch {
            // Storage that cannot be written only brings the hint back next load.
          }
          onDismiss()
        }}>Dismiss</Button>
      )}
    >
      Needs iOS 16.4 or later. In Safari, tap <b>Share</b> (the square with an arrow; under ⋯ if hidden), then <b>Add to Home Screen</b>. Chrome on iOS can add it too. Open Aether from the new icon and turn this on there.
    </Callout>
  )
}

export function PushSection({ client, onSubscriptionChange }: { client: Api; onSubscriptionChange?: () => void }) {
  const gateway = useStore((s) => s.capabilities?.gateway)
  const [state, setState] = useState<PushState | null>(null)
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [dismissed, setDismissed] = useState(hintDismissed)

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
  const install = state?.kind === 'unsupported' && state.why === 'home-screen'
  return (
    <SettingsSection title="Push notifications" id="push" scope="This device">
      <SettingRow
        label="Notify this device when a session needs me"
        labelFor={install ? undefined : 'settings-push'}
        help={help(state, install && !dismissed)}
        control={install ? undefined : (
          <Checkbox
            id="settings-push"
            checked={on}
            disabled={busy || (state?.kind !== 'on' && state?.kind !== 'off')}
            onCheckedChange={(checked) => act((checked === true ? enablePush(client) : disablePush(client)).finally(() => onSubscriptionChange?.()))}
          />
        )}
      >
        {install && !dismissed && <InstallHint onDismiss={() => {
          setDismissed(true)
          // The callout holding focus is gone: land on the guide link its absence brings back.
          requestAnimationFrame(() => document.querySelector<HTMLElement>('#push a[href*="networking.md"]')?.focus())
        }} />}
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

const browserZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const quietDefault = () => ({ start: '22:00', end: '07:00', tz: browserZone() })

const later = 'finished, failed, stopped, merged, checks, reviews and closed pull requests'


type Row = 'watch-push' | 'level' | 'quiet-hours'

/** unavailable is the server's own error from push.status, such as Web Push not being configured. */
function destinations(prefs: NotifyPrefs, unavailable: string | null): string {
  const devices = prefs.destinations.push_devices
  const push = !prefs.push
    ? 'Push is off'
    : unavailable
      ? `No push (${unavailable})`
      : devices === 0
        ? 'No push device subscribed'
        : `Push to ${devices} ${devices === 1 ? 'device' : 'devices'}`
  const desktop = desktopBridge() ? 'this desktop app while it is open' : 'the desktop app while it is open'
  return `${push} · ${desktop} · in-app on every dashboard.`
}

/** Watch notices: whether they push, and the hours they wait. */
/** subscriptions changes whenever this device subscribes or unsubscribes, so the device count is re-read. */
export function WatchNotifySection({ client, subscriptions = 0 }: { client: Api; subscriptions?: number }) {
  const cap = useStore((s) => s.capabilities)
  const [prefs, setPrefs] = useState<NotifyPrefs | null>(null)
  const [quiet, setQuiet] = useState(quietDefault)
  const [digestAt, setDigestAt] = useState('09:00')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<{ row: Row; text: string } | null>(null)
  const [unavailable, setUnavailable] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)
  const supported = cap === null || cap.methods.includes('*') || cap.methods.includes('notify.prefs.get')

  useEffect(() => {
    if (!supported) return
    let live = true
    client.notifyPrefsGet().then(
      (next) => {
        if (!live) return
        setPrefs(next)
        if (next.quiet_hours) setQuiet(next.quiet_hours)
        if (next.digest) setDigestAt(next.digest.at)
      },
      (err: unknown) => live && setError({ row: 'watch-push', text: errorSentence(err) }),
    )
    client.pushStatus().then(
      () => live && setUnavailable(null),
      (err: unknown) => live && setUnavailable(err instanceof ApiError && err.code === codeUnavailable ? errorSentence(err) : null),
    )
    return () => {
      live = false
    }
  }, [client, attempt, supported, subscriptions])

  if (!supported) return null

  const save = (next: NotifyPrefsSetParams, row: Row) => {
    setBusy(true)
    setError(null)
    client.notifyPrefsSet(next)
      .then((saved) => {
        setPrefs(saved)
        if (saved.quiet_hours) setQuiet(saved.quiet_hours)
        if (saved.digest) setDigestAt(saved.digest.at)
      }, (err: unknown) => setError({ row, text: errorSentence(err) }))
      .finally(() => setBusy(false))
  }

  const held = prefs?.quiet_hours
  const edited = held && (held.start !== quiet.start || held.end !== quiet.end || held.tz !== quiet.tz)
  const level = prefs?.level ?? 'batched'
  const savedDigest = prefs?.digest ?? { at: '09:00', tz: '' }
  const savedZone = savedDigest.tz || 'UTC'
  const here = browserZone()
  return (
    <SettingsSection title="When to notify" scope="Your account">
      <SettingRow
        id="level"
        label={<span id="settings-level-label">How often</span>}
        help={(
          <>
            <Guide page="failure-handling" to="needs-you">Needs you</Guide> is what waits on you: a question, a permission, a blocked or failed session.
            {prefs?.destinations.push_devices === 0 && ' No device receives pushes yet; this takes effect once one does.'}
          </>
        )}
      >
        <RadioGroup
          aria-labelledby="settings-level-label"
          value={level}
          disabled={busy || !prefs}
          onValueChange={(next) => prefs && save({ level: next as NotifyLevel, quiet_hours: prefs.quiet_hours }, 'level')}
        >
          {([
            { level: 'blockers', label: 'Blockers now, the rest daily', help: `Needs you at once, except during quiet hours; ${later} in one digest daily at ${savedDigest.at}.` },
            { level: 'batched', label: 'Blockers now, the rest every half hour', help: `Needs you at once, except during quiet hours; ${later} in a digest on each half hour.` },
            { level: 'all', label: 'Everything at once', help: 'Every push as it happens, except during quiet hours.' },
          ] satisfies { level: NotifyLevel; label: string; help: string }[]).map((l) => (
            <label key={l.level} className="flex min-w-0 cursor-pointer items-start gap-2 py-1 coarse:min-h-11">
              <Radio value={l.level} aria-labelledby={`settings-level-${l.level}-label`} aria-describedby={`settings-level-${l.level}`} className="mt-0.5" />
              <span className="flex min-w-0 flex-col gap-0.5">
                <span id={`settings-level-${l.level}-label`} className="text-ui text-text">{l.label}{l.level === 'batched' && <span className="text-muted"> (default)</span>}</span>
                <span id={`settings-level-${l.level}`} className="text-ui-sm text-muted">{l.help}</span>
              </span>
            </label>
          ))}
        </RadioGroup>
        {level === 'blockers' && (
          <div className="flex min-w-0 flex-col gap-1">
            <label htmlFor="settings-digest-at" className="text-ui-sm text-muted">Daily digest at ({savedZone})</label>
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <Input id="settings-digest-at" type="time" required value={digestAt} disabled={busy} className="w-28" onChange={(event) => setDigestAt(event.target.value)} />
              {digestAt !== savedDigest.at && (
                <Button size="sm" variant="secondary" disabled={busy || !digestAt} onClick={() => prefs && save({ digest: { at: digestAt, tz: savedDigest.tz }, quiet_hours: prefs.quiet_hours }, 'level')}>
                  Save digest time
                </Button>
              )}
              {here !== savedZone && (
                <Button size="sm" variant="secondary" disabled={busy} onClick={() => prefs && save({ digest: { at: digestAt || savedDigest.at, tz: here }, quiet_hours: prefs.quiet_hours }, 'level')}>
                  Use this browser’s zone ({here})
                </Button>
              )}
            </div>
          </div>
        )}
        {error?.row === 'level' && <Callout tone="failed" role="alert">{error.text}</Callout>}
      </SettingRow>
      <SettingRow
        id="watch-push"
        label="Watch notifications on push devices"
        labelFor="settings-watch-push"
        help={prefs ? destinations(prefs, unavailable) : error ? null : 'Reading your preferences…'}
        control={<Checkbox id="settings-watch-push" checked={prefs?.push ?? false} disabled={busy || !prefs} onCheckedChange={(on) => prefs && save({ push: on === true, quiet_hours: prefs.quiet_hours }, 'watch-push')} />}
      >
        {error?.row === 'watch-push' && <Callout tone="failed" role="alert">{error.text}</Callout>}
        {error && !prefs && <TryAgain onClick={() => { setError(null); setAttempt((n) => n + 1) }} />}
      </SettingRow>
      <SettingRow
        id="quiet-hours"
        label="Quiet hours"
        labelFor="settings-quiet-hours"
        help="Every push waits, Needs you included, then arrives in one digest when they end. In-app notices and the desktop app are not held."
        control={(
          <Checkbox
            id="settings-quiet-hours"
            checked={Boolean(held)}
            disabled={busy || !prefs}
            onCheckedChange={(on) => prefs && save({ quiet_hours: on === true ? quiet : undefined }, 'quiet-hours')}
          />
        )}
      >
        {held && (
          <div className="flex min-w-0 flex-wrap items-start gap-3">
            <label className="flex flex-col gap-1 text-ui-sm text-muted">
              From
              <Input type="time" required value={quiet.start} disabled={busy} className="w-28" onChange={(event) => setQuiet({ ...quiet, start: event.target.value })} />
            </label>
            <label className="flex flex-col gap-1 text-ui-sm text-muted">
              Until
              <Input type="time" required value={quiet.end} disabled={busy} className="w-28" onChange={(event) => setQuiet({ ...quiet, end: event.target.value })} />
            </label>
            <div className="flex min-w-0 flex-col gap-1 text-ui-sm text-muted">
              <label className="flex min-w-0 flex-col gap-1">
                Time zone
                <Input
                  value={quiet.tz}
                  disabled={busy}
                  className="w-52 max-w-full"
                  spellCheck={false}
                  list="settings-quiet-zones"
                  aria-invalid={error?.row === 'quiet-hours' || undefined}
                  aria-describedby={error?.row === 'quiet-hours' ? 'settings-quiet-error' : 'settings-quiet-zone-help'}
                  onChange={(event) => setQuiet({ ...quiet, tz: event.target.value })}
                />
              </label>
              <span id="settings-quiet-zone-help">An IANA zone, such as Europe/Berlin.</span>
              <datalist id="settings-quiet-zones">
                {Intl.supportedValuesOf('timeZone').map((zone) => <option key={zone} value={zone} />)}
              </datalist>
            </div>
            {edited && (
              <div className="basis-full">
                <Button size="sm" variant="secondary" disabled={busy || !quiet.start || !quiet.end || !quiet.tz.trim()} onClick={() => prefs && save({ quiet_hours: { ...quiet, tz: quiet.tz.trim() } }, 'quiet-hours')}>Save quiet hours</Button>
              </div>
            )}
          </div>
        )}
        {error?.row === 'quiet-hours' && <Callout id="settings-quiet-error" tone="failed" role="alert">{error.text}</Callout>}
      </SettingRow>
    </SettingsSection>
  )
}
