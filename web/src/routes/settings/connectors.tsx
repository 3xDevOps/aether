import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Confirm } from '@/components/confirm'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { RelativeTime } from '@/components/ui/relative-time'
import type { Api } from '@/lib/api'
import { clockTime, errorSentence } from '@/lib/format'
import type { Connector, ConnectorCode } from '@/lib/types'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'

const pollMs = 3000

function Link({ href, children }: { href: string; children: ReactNode }) {
  return <a href={href} target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">{children}</a>
}

function label(c: Connector): string {
  return `${c.client_name} (${c.redirect_host})`
}

function Steps({ code }: { code: ConnectorCode }) {
  return (
    <div className="flex min-w-0 flex-col gap-3 text-ui-sm">
      <div className="flex min-w-0 flex-col gap-1">
        <span className="text-muted">URL</span>
        <CopyableCommand command={code.url} />
      </div>
      <div className="flex min-w-0 flex-col gap-1">
        <span className="text-muted">Code, one use, until {clockTime(code.expires_at)}</span>
        <CopyableCommand command={code.code} />
      </div>
      <div className="flex min-w-0 flex-col gap-1">
        <p className="font-medium text-text">ChatGPT</p>
        <ol className="list-decimal pl-5 text-muted">
          <li>Open <Link href="https://chatgpt.com/plugins">ChatGPT Plugins</Link>, select <strong>+</strong>, then <strong>Add custom MCP server</strong>.</li>
          <li>Paste the URL, select <strong>Create as a plugin</strong> and install it.</li>
          <li>On the page that opens, paste the code, choose scopes and select <strong>Allow</strong>.</li>
        </ol>
      </div>
      <div className="flex min-w-0 flex-col gap-1">
        <p className="font-medium text-text">Claude</p>
        <ol className="list-decimal pl-5 text-muted">
          <li>Open <Link href="https://claude.ai/settings/connectors">Claude connectors</Link> and select <strong>Add custom connector</strong>.</li>
          <li>Paste the URL and select <strong>Connect</strong>.</li>
          <li>On the page that opens, paste the code, choose scopes and select <strong>Allow</strong>.</li>
        </ol>
        <p className="text-muted">On Team and Enterprise, an Owner adds the URL under <strong>Organization settings → Connectors</strong>; you then select <strong>Connect</strong> on it and paste the code.</p>
      </div>
    </div>
  )
}

export function ConnectorsSection({ client }: { client: Api }) {
  const [connectors, setConnectors] = useState<Connector[] | null>(null)
  const [attempt, setAttempt] = useState<{ code: ConnectorCode; known: Set<string> } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<Connector | null>(null)

  const read = useCallback(() => client.connectorList().then((r) => r.connectors), [client])

  useEffect(() => {
    let live = true
    read().then(
      (next) => live && setConnectors(next),
      (err: unknown) => live && setError(errorSentence(err)),
    )
    return () => {
      live = false
    }
  }, [read])

  useEffect(() => {
    if (!attempt) return
    let live = true
    let polling = false
    const expiry = setTimeout(() => setAttempt(null), Date.parse(attempt.code.expires_at) - Date.now())
    const timer = setInterval(() => {
      if (polling) return
      polling = true
      read().then((next) => {
        const added = next.find((c) => !attempt.known.has(c.id))
        if (!live || !added) return
        setConnectors(next)
        setAttempt(null)
        toast.success(`${label(added)} connected`)
      }, () => {}).finally(() => {
        polling = false
      })
    }, pollMs)
    return () => {
      live = false
      clearTimeout(expiry)
      clearInterval(timer)
    }
  }, [attempt, read])

  const mint = () => {
    setBusy(true)
    setError(null)
    read()
      .then(async (fresh) => {
        setConnectors(fresh)
        const code = await client.connectorCode()
        setAttempt({ code, known: new Set(fresh.map((c) => c.id)) })
      })
      .catch((err: unknown) => setError(errorSentence(err)))
      .finally(() => setBusy(false))
  }

  return (
    <SettingsSection title="ChatGPT and Claude">
      <SettingRow
        label="Connect ChatGPT or Claude"
        help={<>Launch runs, see what needs you and answer agents from a chat. The connector acts as you. <Link href="https://github.com/3xDevOps/Aether/blob/main/docs/connectors.md">Connector guide</Link></>}
        control={<Button size="sm" variant="secondary" disabled={busy} onClick={mint}>{attempt ? 'New code' : 'Get a code'}</Button>}
      >
        {attempt && <Steps code={attempt.code} />}
        {error && <Callout tone="failed" role="alert" className="whitespace-pre-wrap">{error}</Callout>}
      </SettingRow>
      {connectors?.map((c) => (
        <SettingRow
          key={c.id}
          label={label(c)}
          help={(
            <>
              <Code>{c.scopes.join(', ')}</Code>
              {' · added '}<RelativeTime at={c.created_at} />
              {' · '}{c.last_used_at ? <>last used <RelativeTime at={c.last_used_at} /></> : 'never used'}
            </>
          )}
          control={<Button size="sm" variant="ghost" aria-label={`Revoke ${label(c)}`} onClick={() => setRevoking(c)}>Revoke</Button>}
        />
      ))}
      {revoking && (
        <Confirm
          title={`Revoke ${label(revoking)}?`}
          description={`Its next call fails, and ${revoking.client_name} asks you to connect again with a new code. Removing it in ${revoking.client_name} does not revoke it here.`}
          action="Revoke"
          pending="Revoking…"
          onConfirm={() => client.connectorRevoke(revoking.id)}
          onDone={() => {
            toast.success(`${label(revoking)} revoked`)
            void read().then(setConnectors, (err: unknown) => setError(errorSentence(err)))
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </SettingsSection>
  )
}
