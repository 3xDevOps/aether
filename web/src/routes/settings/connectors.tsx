import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Confirm } from '@/components/confirm'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { RelativeTime } from '@/components/ui/relative-time'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { type Api, ApiError, codeMethodNotFound } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { useCountdown } from '@/lib/hooks'
import type { Connector, ConnectorCode, ServerEdge } from '@/lib/types'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

/** The section's element id and its `section` route param. */
export const CONNECTORS_SECTION_ID = 'connectors'

const pollMs = 3000
const docs = 'https://github.com/3xDevOps/Aether/blob/main/docs'

type App = 'chatgpt' | 'claude'

const scopeNames: Record<string, string> = { read: 'Read', write: 'Launch and message', control: 'Stop and approve' }

const scopes: [string, string][] = [
  ['Read, always', 'Workspaces, agents, every member’s runs and their history, swarms, pull requests and team memory.'],
  ['Launch and message, checked by default', 'Launch runs and swarms; message, pause and resume runs; answer agents and teammates; mark runs reviewed.'],
  ['Stop and approve, unchecked by default', 'Decide permission requests, stop and close runs, cancel swarms. Text an agent wrote could talk the assistant into approving or ending work without asking you.'],
]

type Failure = { text: string; fix: ReactNode; old?: boolean }

function Link({ href, children }: { href: string; children: ReactNode }) {
  return <a href={href} target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">{children}</a>
}

function failure(err: unknown): Failure {
  const text = errorSentence(err)
  if (err instanceof ApiError && err.code === codeMethodNotFound) {
    return { text, old: true, fix: <>This server is older than connectors. An admin updates it in <strong>Settings › Server</strong> or with <Code>sudo aether-server update</Code>.</> }
  }
  if (/\bedge\b/.test(text)) return { text, fix: <Link href={`${docs}/edge.md#turning-it-on`}>Edge guide</Link> }
  return { text, fix: <Link href={`${docs}/connectors.md#troubleshooting`}>Connector troubleshooting</Link> }
}

function FailureCallout({ failure: { text, fix } }: { failure: Failure }) {
  return (
    <Callout tone="failed" role="alert">
      <p className="whitespace-pre-wrap">{text}</p>
      <p className="mt-1 text-ui-sm">{fix}</p>
    </Callout>
  )
}

function label(c: Connector): string {
  return `${c.client_name} (${c.redirect_host})`
}

function appOf(c: Connector): App {
  return c.redirect_host === 'claude.ai' ? 'claude' : 'chatgpt'
}

function scopeList(c: Connector): string {
  return c.scopes.map((s) => scopeNames[s] ?? s).join(', ')
}

function Step({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="flex min-w-0 gap-2">
      <span aria-hidden className="grid size-5 shrink-0 place-items-center rounded-full border border-seam text-ui-xs tabular-nums text-muted">{n}</span>
      <div className="flex min-w-0 flex-1 flex-col gap-1.5">{children}</div>
    </li>
  )
}

function CodeStep({ code, busy, expired, onMint }: { code: ConnectorCode | null; busy: boolean; expired: boolean; onMint: () => void }) {
  const countdown = useCountdown(code?.expires_at)
  if (!code) {
    return (
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <Button size="sm" disabled={busy} onClick={onMint}>{busy ? 'Getting a code…' : 'Get a code'}</Button>
        <span className="text-muted">{expired ? 'The last code expired. ' : ''}A code works once, for 10 minutes.</span>
      </div>
    )
  }
  return (
    <>
      <CopyableCommand command={code.code} />
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <p role="timer" aria-live="off" className="tabular-nums text-muted">One use, {countdown}.</p>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onMint}>New code</Button>
      </div>
    </>
  )
}

const guides: Record<App, { open: ReactNode; paste: ReactNode; note: string }> = {
  chatgpt: {
    open: <>On chatgpt.com, open <Link href="https://chatgpt.com/plugins">ChatGPT Plugins</Link>, select <strong>+</strong>, then <strong>Add custom MCP server</strong>.</>,
    paste: <>Name it <strong>Aether</strong>, paste this URL as the server URL, choose <strong>OAuth</strong>, then select <strong>Create as a plugin</strong> and install it.</>,
    note: 'Write tools need ChatGPT Business or Enterprise/Edu; Pro gets the read tools only. Once added, it works in ChatGPT on the web, desktop and phone.',
  },
  claude: {
    open: <>Open <Link href="https://claude.ai/settings/connectors">Claude connectors</Link> (<strong>Customize → Connectors</strong>) and select <strong>Add custom connector</strong>. On Team and Enterprise, an Owner adds it first under <strong>Organization settings → Connectors</strong>.</>,
    paste: <>Name it <strong>Aether</strong>, paste this URL, leave <strong>Advanced settings</strong> empty, select <strong>Add</strong>, then <strong>Connect</strong>.</>,
    note: 'Free allows one custom connector. Once added, it works in Claude on the web, desktop and phone.',
  },
}

function Setup({
  app,
  url,
  edgeNote,
  code,
  busy,
  expired,
  onMint,
}: {
  app: App
  url: string | null
  edgeNote: ReactNode
  code: ConnectorCode | null
  busy: boolean
  expired: boolean
  onMint: () => void
}) {
  const guide = guides[app]
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <ol className="flex min-w-0 flex-col gap-3">
        <Step n={1}><p>{guide.open}</p></Step>
        <Step n={2}>
          <p>{guide.paste}</p>
          {url ? <CopyableCommand command={url} /> : edgeNote ?? <p className="text-muted">The URL appears with the code.</p>}
        </Step>
        <Step n={3}>
          <p>On the Aether page that opens, choose the connector's scopes, paste this code if the page asks for one, and select <strong>Allow</strong>.</p>
          <CodeStep code={code} busy={busy} expired={expired} onMint={onMint} />
        </Step>
      </ol>
      <p className="text-muted">{guide.note}</p>
    </div>
  )
}

export function ConnectorsSection({ client }: { client: Api }) {
  const servesEdge = useCapability().hasMethod('server.edge')
  const route = useStore((s) => s.route)
  const [connectors, setConnectors] = useState<Connector[] | null>(null)
  const [listError, setListError] = useState<Failure | null>(null)
  const [edge, setEdge] = useState<ServerEdge | null>(null)
  const [edgeError, setEdgeError] = useState<string | null>(null)
  const [app, setApp] = useState<App>('chatgpt')
  const [attempt, setAttempt] = useState<{ code: ConnectorCode; known: Set<string> } | null>(null)
  const [expired, setExpired] = useState(false)
  const [connected, setConnected] = useState<Connector | null>(null)
  const [busy, setBusy] = useState(false)
  const [mintError, setMintError] = useState<Failure | null>(null)
  const [confirming, setConfirming] = useState<{ connector: Connector; reconnect: boolean } | null>(null)

  const read = useCallback(() => client.connectorList().then((r) => r.connectors), [client])

  const loaded = connectors !== null || listError !== null
  useEffect(() => {
    if (loaded && route.params.section === CONNECTORS_SECTION_ID) document.getElementById(CONNECTORS_SECTION_ID)?.scrollIntoView({ block: 'start' })
  }, [route, loaded])

  useEffect(() => {
    let live = true
    read().then(
      (next) => live && setConnectors(next),
      (err: unknown) => live && setListError(failure(err)),
    )
    if (servesEdge) {
      client.serverEdge().then(
        (next) => live && setEdge(next),
        (err: unknown) => live && setEdgeError(errorSentence(err)),
      )
    }
    return () => {
      live = false
    }
  }, [client, read, servesEdge])

  useEffect(() => {
    if (!attempt) return
    let live = true
    let polling = false
    const expiry = setTimeout(() => {
      setAttempt(null)
      setExpired(true)
    }, Date.parse(attempt.code.expires_at) - Date.now())
    const timer = setInterval(() => {
      if (polling) return
      polling = true
      read().then((next) => {
        const added = next.find((c) => !attempt.known.has(c.id))
        if (!live || !added) return
        setConnectors(next)
        setAttempt(null)
        setConnected(added)
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

  const mint = async () => {
    setBusy(true)
    setMintError(null)
    setConnected(null)
    setExpired(false)
    try {
      const fresh = await read()
      setConnectors(fresh)
      setListError(null)
      const code = await client.connectorCode()
      setAttempt({ code, known: new Set(fresh.map((c) => c.id)) })
    } catch (err) {
      setMintError(failure(err))
    } finally {
      setBusy(false)
    }
  }

  const tooOld = listError?.old === true
  const edgeNote = edge && !edge.edge_url
    ? <p className="text-muted">This server has no edge, so ChatGPT and Claude cannot reach it. An admin turns one on: <Link href={`${docs}/edge.md#turning-it-on`}>Edge guide</Link>.</p>
    : edgeError && <p className="text-muted">The URL appears with the code. Reading the server's edge failed: {edgeError}</p>
  const offline = edge?.edge_url && !edge.connected
    ? <p className="text-muted">This server is not connected to its edge at {edge.edge_url}; an admin sees why with <Code>sudo aether-server edge status</Code>.</p>
    : null
  const appName = app === 'chatgpt' ? 'ChatGPT' : 'Claude'

  return (
    <SettingsSection title="ChatGPT and Claude" id={CONNECTORS_SECTION_ID}>
      <SettingRow
        label="Connect ChatGPT or Claude"
        help={<>Launch runs, see what needs you and answer agents from a chat, on your phone too. The connector acts as you. <Link href={`${docs}/connectors.md`}>Connector guide</Link></>}
      >
        {listError && <FailureCallout failure={listError} />}
        {!tooOld && (
          <Tabs value={app} onValueChange={(next) => setApp(next as App)} activationMode="automatic" className="flex min-w-0 flex-col gap-3 text-ui-sm">
            <TabsList look="segmented" aria-label="App" className="self-start">
              <TabsTrigger value="chatgpt">ChatGPT</TabsTrigger>
              <TabsTrigger value="claude">Claude</TabsTrigger>
            </TabsList>
            {(['chatgpt', 'claude'] as const).map((value) => (
              <TabsContent key={value} value={value}>
                <Setup app={value} url={attempt?.code.url ?? null} edgeNote={edgeNote} code={attempt?.code ?? null} busy={busy} expired={expired} onMint={() => void mint()} />
              </TabsContent>
            ))}
            {offline}
            {attempt && (
              <p className="flex items-center gap-1.5 text-muted" role="status">
                <Spinner />
                Waiting for {appName} to connect…
              </p>
            )}
          </Tabs>
        )}
        {mintError && <FailureCallout failure={mintError} />}
        {connected && (
          <Callout
            tone="done"
            role="status"
            title={`${label(connected)} connected`}
            actions={<Button size="sm" variant="secondary" onClick={() => setConnected(null)}>Done</Button>}
          >
            With {scopeList(connected)}. Ask it “What needs me in Aether?”
          </Callout>
        )}
      </SettingRow>
      <SettingRow
        label="What a connector may do"
        help="You choose its scopes on the Aether page that opens when you connect; ChatGPT and Claude do not. To change them, reconnect."
      >
        <dl className="flex min-w-0 flex-col gap-2 text-ui-sm">
          {scopes.map(([name, text]) => (
            <div key={name} className="flex min-w-0 flex-col">
              <dt className="font-medium text-text">{name}</dt>
              <dd className="text-muted">{text}</dd>
            </div>
          ))}
        </dl>
      </SettingRow>
      {connectors?.length === 0 && (
        <SettingRow label="No connectors yet" help="One appears here once you select Allow on the Aether page." />
      )}
      {connectors?.map((c) => (
        <SettingRow
          key={c.id}
          label={label(c)}
          help={(
            <>
              <span className="block">{scopeList(c)}</span>
              Added <RelativeTime at={c.created_at} />
              {' · '}{c.last_used_at ? <>last used <RelativeTime at={c.last_used_at} /></> : 'never used'}
            </>
          )}
          control={(
            <>
              <Button size="sm" variant="ghost" aria-label={`Reconnect ${label(c)}`} disabled={busy} onClick={() => setConfirming({ connector: c, reconnect: true })}>Reconnect</Button>
              <Button size="sm" variant="ghost" aria-label={`Revoke ${label(c)}`} disabled={busy} onClick={() => setConfirming({ connector: c, reconnect: false })}>Revoke</Button>
            </>
          )}
        />
      ))}
      {confirming && (confirming.reconnect ? (
        <Confirm
          title={`Reconnect ${label(confirming.connector)}?`}
          description={`This revokes it now and gets a new code. Then connect Aether again in ${confirming.connector.client_name} and paste the code; you can choose other scopes.`}
          action="Reconnect"
          pending="Revoking…"
          onConfirm={async () => {
            await client.connectorRevoke(confirming.connector.id)
            setApp(appOf(confirming.connector))
          }}
          onDone={() => void mint()}
          onClose={() => setConfirming(null)}
        />
      ) : (
        <Confirm
          title={`Revoke ${label(confirming.connector)}?`}
          description={`Its next call fails, and ${confirming.connector.client_name} asks you to connect again with a new code. Removing it in ${confirming.connector.client_name} does not revoke it here.`}
          action="Revoke"
          pending="Revoking…"
          onConfirm={() => client.connectorRevoke(confirming.connector.id)}
          onDone={() => {
            toast.success(`${label(confirming.connector)} revoked`)
            read().then((next) => {
              setConnectors(next)
              setListError(null)
            }, (err: unknown) => setListError(failure(err)))
          }}
          onClose={() => setConfirming(null)}
        />
      ))}
    </SettingsSection>
  )
}
