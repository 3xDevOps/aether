import { type ReactNode, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api, type Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { useStore } from '@/store'
import { saveChildSettings, useChildSettingsRead } from '@/store/children'
import { useCapability } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

type Answer = 'allow' | 'later' | 'never'

function useAnswer(client: Api, allow?: () => Promise<unknown>) {
  const [answered, setAnswered] = useState<Answer | null>(null)
  const [sending, setSending] = useState<Answer | null>(null)
  const [error, setError] = useState<string | null>(null)
  const answer = (choice: Answer) => {
    setSending(choice)
    setError(null)
    const sent = choice === 'allow' ? allow!() : saveChildSettings({ nudge: choice }, client)
    sent.then(() => setAnswered(choice), (err: unknown) => setError(errorSentence(err))).finally(() => setSending(null))
  }
  return { answered, sending, error, answer }
}

function SettingsLink({ children, onLeave }: { children: ReactNode; onLeave?: () => void }) {
  const navigate = useStore((s) => s.navigate)
  const open = () => {
    onLeave?.()
    navigate('settings', { section: 'children' })
  }
  return (
    <Button variant="link" size="sm" className="h-auto p-0" onClick={open}>
      {children}
    </Button>
  )
}

/** Shown on a run whose agent tried to start a child run while child runs were off. It never turns on the member setting. */
export function RunChildNudge({ run, client = api }: { run: RunRecord; client?: Api }) {
  const cap = useCapability()
  const self = useStore((s) => s.info?.member.id)
  const settings = useChildSettingsRead(client)
  const { answered, sending, error, answer } = useAnswer(client, () => client.runChildrenAllow(run.id, 'on'))
  if (answered) {
    return (
      <p role="status" className="border-b border-seam px-4 py-2 text-ui-sm text-muted">
        {answered === 'allow' && <>Child runs are allowed for this run. Ask the agent to try again.</>}
        {answered === 'later' && <>You can turn child runs on later in <SettingsLink>Settings → Child runs</SettingsLink>.</>}
        {answered === 'never' && <>We won&apos;t suggest this again. Child runs stay available in <SettingsLink>Settings → Child runs</SettingsLink>.</>}
      </p>
    )
  }
  const shown = run.child_refused && !run.children && run.member_id === self && !run.mission_id && !isTerminal(run.status) &&
    cap.hasMethod('run.children.allow') && settings?.nudge === 'show' && !settings.enabled
  if (!shown) return null
  const button = (choice: Answer, label: string) => (
    <Button size="sm" variant="secondary" disabled={sending !== null} onClick={() => answer(choice)}>{label}</Button>
  )
  return (
    <Callout
      tone="neutral"
      role="group"
      aria-label="Let this run start child runs?"
      title="The agent tried to start a child run"
      className="mx-4 my-2 border border-seam"
      actions={(
        <>
          {button('allow', sending === 'allow' ? 'Allowing…' : 'Allow for this run')}
          <SettingsLink>Open settings</SettingsLink>
          {button('later', 'Not now')}
          {button('never', "Don't show again")}
        </>
      )}
    >
      <p>Child runs are off, so it was refused. A child run is another agent this run starts on your account, for example a review on another model. Allowing it here covers this run and the runs it starts, within the workspace limits.</p>
      {error && <p role="alert" className="mt-1 text-state-failed">{error}</p>}
    </Callout>
  )
}

/** A one-line hint in the launch dialog when more than one agent could be delegated to. */
export function LaunchChildHint({ launchable, onLeave, client = api }: { launchable: number; onLeave: () => void; client?: Api }) {
  const settings = useChildSettingsRead(client)
  const { answered, sending, error, answer } = useAnswer(client)
  if (answered || launchable < 2 || settings?.nudge !== 'show' || settings.enabled) return null
  return (
    <p className="flex flex-wrap items-center gap-x-2 text-ui-sm text-muted">
      <span>Agents can start each other as child runs, for example a Codex review of a Claude change: <SettingsLink onLeave={onLeave}>Settings → Child runs</SettingsLink>.</span>
      <Button variant="link" size="sm" className="h-auto p-0" disabled={sending !== null} onClick={() => answer('later')}>Not now</Button>
      <Button variant="link" size="sm" className="h-auto p-0" disabled={sending !== null} onClick={() => answer('never')}>Don&apos;t show again</Button>
      {error && <span role="alert" className="text-state-failed">{error}</span>}
    </p>
  )
}
