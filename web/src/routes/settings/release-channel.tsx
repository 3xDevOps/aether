import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button, buttonVariants } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { RadioPills, RadioPillsItem } from '@/components/ui/radio-pills'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { ChannelPointer, ReleaseChannel, ServerReleaseChannelSet } from '@/lib/types'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability, useIsSuperAdmin } from '@/store/hooks'
import { readServerStatus } from '@/store/updates'

const channels: ReleaseChannel[] = ['stable', 'beta', 'nightly']

const label: Record<ReleaseChannel, string> = { stable: 'Stable', beta: 'Beta', nightly: 'Nightly' }

/** The Promote workflow's page; its Run button is what promotes. */
export const promoteURL = 'https://github.com/3xDevOps/aether/actions/workflows/promote.yml'

export type Confirmation = 'nightly' | 'wait'

/**
 * What to ask before saving `channel`: nightly always asks; any other
 * channel asks only when the dry run says the server would wait, or could
 * not read the channel's newest release to say.
 */
export function confirmation(channel: ReleaseChannel, dry?: ServerReleaseChannelSet): Confirmation | null {
  if (channel === 'nightly') return 'nightly'
  if (dry && (dry.wait || dry.target_error)) return 'wait'
  return null
}

export function ReleaseChannelSection({ client = api }: { client?: Api } = {}) {
  const status = useStore((s) => s.serverUpdate)
  const superAdmin = useIsSuperAdmin()
  const offered = useCapability().hasMethod('server.release_channel_set')
  const [asking, setAsking] = useState<{ channel: ReleaseChannel; kind: Confirmation; dry?: ServerReleaseChannelSet } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState('')
  const current = status?.channel
  if (!offered) return null
  if (!status) {
    return (
      <SettingsSection title="Release channel" scope="Server">
        <SettingRow id="release-channel" label="Release channel" help="Reading the release channel…" />
      </SettingsSection>
    )
  }
  if (!current) return null
  const canSet = superAdmin

  const save = async (channel: ReleaseChannel) => {
    setBusy(true)
    setError(null)
    try {
      const saved = await client.serverReleaseChannelSet(channel)
      setNotice(saved.retired ? `The pending update to ${saved.retired} was cancelled: the ${label[channel]} channel does not take it.` : '')
      await readServerStatus(useStore, client)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const choose = async (channel: ReleaseChannel) => {
    if (channel === current) return
    setError(null)
    setNotice('')
    if (channel === 'nightly') {
      setAsking({ channel, kind: 'nightly' })
      return
    }
    setBusy(true)
    try {
      const dry = await client.serverReleaseChannelSet(channel, true)
      const kind = confirmation(channel, dry)
      if (kind) setAsking({ channel, kind, dry })
      else await save(channel)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const promotable = canSet && (current === 'nightly' || current === 'beta')

  return (
    <SettingsSection title="Release channel" scope={canSet ? 'Server · super admin' : 'Server'}>
      <SettingRow
        id="release-channel"
        label="Channel"
        help={[
          'The server, this computer\'s app and the CLI follow it; the Android app follows its server.',
          canSet ? '' : 'Only a super admin can change it.',
          status.channel_pending ? 'This server has not chosen a channel yet and follows beta.' : '',
        ].filter(Boolean).join(' ')}
        control={
          canSet ? (
            <RadioPills aria-label="Release channel" value={current} disabled={busy} onValueChange={(v) => void choose(v as ReleaseChannel)}>
              {channels.map((c) => (
                <RadioPillsItem key={c} value={c}>{label[c]}</RadioPillsItem>
              ))}
            </RadioPills>
          ) : (
            <Badge>{label[current]}</Badge>
          )
        }
      >
        {notice && <p role="status" className="text-ui-sm text-muted">{notice}</p>}
        {error && <p role="alert" className="max-h-28 overflow-auto whitespace-pre-wrap break-words rounded-control border border-state-failed/30 bg-state-failed/5 px-2 py-1.5 font-code text-ui-sm leading-5 text-state-failed select-text">{error}</p>}
      </SettingRow>
      {promotable &&
        (['beta', 'nightly'] as const).map((c) => {
          const pointer = status.channel_pointers?.[c]
          return pointer && <PromoteRow key={c} channel={c} pointer={pointer} />
        })}
      <Dialog open={asking !== null} onOpenChange={(open) => !open && setAsking(null)}>
        <DialogContent>
          {asking?.kind === 'nightly' && <NightlyConfirm />}
          {asking?.kind === 'wait' && <WaitConfirm channel={asking.channel} dry={asking.dry} />}
          <DialogFooter>
            <Button variant="secondary" onClick={() => setAsking(null)}>Cancel</Button>
            <Button
              variant={asking?.kind === 'nightly' ? 'danger' : 'primary'}
              onClick={() => {
                const channel = asking?.channel
                setAsking(null)
                if (channel) void save(channel)
              }}
            >
              Switch to {asking ? label[asking.channel].toLowerCase() : ''}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </SettingsSection>
  )
}

function daysAgo(iso: string, now = Date.now()): string {
  const at = Date.parse(iso)
  if (Number.isNaN(at)) return ''
  const days = Math.floor((now - at) / 86_400_000)
  if (days < 1) return 'today'
  return days === 1 ? '1 day ago' : `${days} days ago`
}

function PromoteRow({ channel, pointer }: { channel: 'beta' | 'nightly'; pointer: ChannelPointer }) {
  const age = daysAgo(pointer.published_at)
  const build = pointer.build ? ` (build ${pointer.build})` : ''
  return (
    <SettingRow
      id={`release-promote-${channel}`}
      label={`Newest ${channel}`}
      help={`${pointer.version}${build}${age ? `, cut ${age}` : ''}. The button opens the Promote workflow on GitHub; its Run button promotes.`}
      control={
        <a className={buttonVariants({ variant: 'secondary', size: 'sm' })} href={promoteURL} target="_blank" rel="noreferrer" aria-label={`Promote the newest ${channel}`}>
          Promote
        </a>
      }
    />
  )
}

function NightlyConfirm() {
  return (
    <DialogHeader>
      <DialogTitle>Switch to nightly builds?</DialogTitle>
      <DialogDescription>Not recommended unless you are developing Aether.</DialogDescription>
      <ul className="list-disc space-y-1 pl-5 text-ui text-muted">
        <li>Nightly builds come from Aether&apos;s main branch. Breaking changes arrive as soon as they merge.</li>
        <li>A nightly build may contain security issues that a later build fixes.</li>
        <li>You cannot go back right away. This server stays on its nightly build until the channel you return to catches up with it.</li>
      </ul>
    </DialogHeader>
  )
}

function WaitConfirm({ channel, dry }: { channel: ReleaseChannel; dry?: ServerReleaseChannelSet }) {
  const name = label[channel]
  const lower = name.toLowerCase()
  const running = dry?.running
  return (
    <DialogHeader>
      <DialogTitle>Switch to {lower}?</DialogTitle>
      <DialogDescription>
        {running && <>This server runs <Code>{running.version}</Code>{running.build > 0 && ` (build ${running.build})`}. </>}
        {dry?.target ? (
          <>
            {name} is at <Code>{dry.target.version}</Code>{dry.target.build > 0 && ` (build ${dry.target.build})`}.
            {' '}The server stays on its current build until {lower} reaches {running && running.build > 0 ? `build ${running.build}` : 'it'}, then updates as usual.
          </>
        ) : (
          <>The newest {lower} release could not be read: {dry?.target_error}</>
        )}
        {' '}It never installs an older build.
      </DialogDescription>
    </DialogHeader>
  )
}
