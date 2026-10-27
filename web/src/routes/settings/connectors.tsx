import { type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
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
import type { Connector, ConnectorCode, ConnectorList, ServerEdge } from '@/lib/types'
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
  if (/has no edge|not connected to its edge/.test(text)) return { text, fix: <Link href={`${docs}/edge.md#turning-it-on`}>Edge guide</Link> }
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

// Claude Code's client id is on claude.ai too, so it opens the Claude tab.
function appOf(c: Connector): App {
  return c.client_id.startsWith('https://chatgpt.com/') ? 'chatgpt' : 'claude'
}

// moveClaims maps a new connector's id to the move that took it as its
// replacement, across tabs, so two moves of the same app never both take
// one connector and revoke two.
const moveClaims = 'aether:connector-move-claims'

function claimMove(id: string, move: string, live: Connector[]): boolean {
  const ids = new Set(live.map((c) => c.id))
  const claims = Object.fromEntries(Object.entries(JSON.parse(localStorage.getItem(moveClaims) ?? '{}') as Record<string, string>).filter(([c]) => ids.has(c)))
  if (claims[id] && claims[id] !== move) return false
  claims[id] = move
  localStorage.setItem(moveClaims, JSON.stringify(claims))
  return true
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

function SignInStep({ watching, busy, onWatch }: { watching: boolean; busy: boolean; onWatch: () => void }) {
  return (
    <>
      <p>On the Aether pages that open, sign in to the edge with GitHub, select <strong>Continue</strong> next to this server, choose its scopes and select <strong>Allow</strong>. No code is needed.</p>
      {!watching && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <Button size="sm" variant="secondary" disabled={busy} onClick={onWatch}>Watch for the connection</Button>
          <span className="text-muted">Shows it here once it connects, for 10 minutes.</span>
        </div>
      )}
    </>
  )
}

function Setup({
  app,
  info,
  edgeNote,
  code,
  at,
  watching,
  busy,
  expired,
  onMint,
}: {
  app: App
  info: Omit<ConnectorList, 'connectors'>
  edgeNote: ReactNode
  code: ConnectorCode | null
  at?: string
  watching: boolean
  busy: boolean
  expired: boolean
  onMint: () => void
}) {
  const guide = guides[app]
  const url = at ?? code?.url ?? info.url ?? null
  const universal = url !== null && url === info.universal_url
  return (
    <div className="flex min-w-0 flex-col gap-3">
      {universal && <p>Install Aether from the {app === 'chatgpt' ? 'ChatGPT' : 'Claude'} directory where it is listed, or add it as a custom connector:</p>}
      <ol className="flex min-w-0 flex-col gap-3">
        <Step n={1}><p>{guide.open}</p></Step>
        <Step n={2}>
          <p>{guide.paste}</p>
          {url ? <CopyableCommand command={url} /> : edgeNote ?? <p className="text-muted">The URL appears with the code.</p>}
          {at && <p className="text-muted">This is this server’s own URL, where the connector you are reconnecting was added. Its page asks for a code.</p>}
          {url && info.universal_url && !universal && !at && (
            <p className="text-muted">
              This is this server’s own URL. To use the one URL <Code>{info.universal_url}</Code> instead, link a GitHub account in <strong>Profile › Sign-ins › Link a GitHub account</strong>, or run <Code>aether member link --github &lt;login&gt;</Code>.
            </p>
          )}
        </Step>
        <Step n={3}>
          {info.sign_in && !at
            ? <SignInStep watching={watching} busy={busy} onWatch={onMint} />
            : (
                <>
                  <p>On the Aether page that opens, choose its scopes, paste this code if the page asks for one, and select <strong>Allow</strong>.</p>
                  <CodeStep code={code} busy={busy} expired={expired} onMint={onMint} />
                </>
              )}
        </Step>
      </ol>
      <p className="text-muted">{guide.note}</p>
    </div>
  )
}

const watchMs = 10 * 60_000

// Keyed by identity: a code and the connector list belong to the member who
// was signed in when they were read.
export function ConnectorsSection({ client }: { client: Api }) {
  return <Connectors key={useStore((s) => s.identityKey)} client={client} />
}

function Connectors({ client }: { client: Api }) {
  const servesEdge = useCapability().hasMethod('server.edge')
  const route = useStore((s) => s.route)
  const [connectors, setConnectors] = useState<Connector[] | null>(null)
  const [info, setInfo] = useState<Omit<ConnectorList, 'connectors'>>({})
  const [listError, setListError] = useState<Failure | null>(null)
  const [edge, setEdge] = useState<ServerEdge | null>(null)
  const [edgeError, setEdgeError] = useState<string | null>(null)
  const [app, setApp] = useState<App>('chatgpt')
  // An attempt watches for a new connector until `until`: a code's expiry,
  // or 10 minutes when signing in replaces the code. `at` is the URL a
  // reconnect connects at when it is not the one shown. A move, `id`,
  // watches only for a connector of `replaces`'s client at `into`, and
  // revokes `replaces` once that one has called Aether.
  const [attempt, setAttempt] = useState<{
    id: string
    code: ConnectorCode | null
    until: string
    app: App
    known: Set<string>
    at?: string
    replaces?: Connector
    into?: string
  } | null>(null)
  const [pollError, setPollError] = useState<Failure | null>(null)
  const [expired, setExpired] = useState(false)
  const [connected, setConnected] = useState<Connector | null>(null)
  const [busy, setBusy] = useState(false)
  const [mintError, setMintError] = useState<Failure | null>(null)
  const [confirming, setConfirming] = useState<{ connector: Connector; action: 'reconnect' | 'move' | 'revoke' } | null>(null)
  const done = useRef<HTMLButtonElement>(null)

  const read = useCallback(() => client.connectorList().then((r) => {
    const { connectors: list, ...rest } = r
    setInfo(rest)
    return list
  }), [client])
  const refresh = () => read().then((next) => {
    setConnectors(next)
    setListError(null)
  }, (err: unknown) => setListError(failure(err)))

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
    if (connected) done.current?.focus()
  }, [connected])

  useEffect(() => {
    if (!attempt) return
    let live = true
    let polling = false
    const settle = (next: Connector[], final: boolean) => {
      if (!live) return
      setPollError(null)
      const old = attempt.replaces
      const finish = () => {
        // The expiry read and a poll can both find it; only one settles.
        live = false
        setConnectors(next)
        setAttempt(null)
      }
      if (!old) {
        const added = next.find((c) => !attempt.known.has(c.id))
        if (added || final) finish()
        if (added) setConnected(added)
        else if (final) setExpired(true)
        return
      }
      const added = next.find((c) => !attempt.known.has(c.id) && c.resource === attempt.into && c.client_id === old.client_id && claimMove(c.id, attempt.id, next))
      if (!added) {
        if (final) {
          finish()
          setExpired(true)
        }
        return
      }
      setConnectors(next)
      setConnected((prev) => (prev?.id === added.id ? prev : added))
      if (added.last_used_at) {
        finish()
        client.connectorRevoke(old.id).then(
          () => setConnectors((list) => list && list.filter((c) => c.id !== old.id)),
          (err: unknown) => setPollError(failure(err)),
        )
      } else if (final) {
        finish()
        setPollError({
          text: `${label(added)} has not called Aether yet, so ${label(old)} was not revoked. Revoke it once the new connection works.`,
          fix: <Link href={`${docs}/connectors.md#move-a-connector-to-the-universal-url`}>Moving a connector</Link>,
        })
      }
    }
    const fail = (err: unknown, final: boolean) => {
      if (!live) return
      setPollError(failure(err))
      if (final) {
        setAttempt(null)
        setExpired(true)
      }
    }
    // A connector authorized just before the code expires still shows.
    const expiry = setTimeout(() => {
      read().then((next) => settle(next, true), (err: unknown) => fail(err, true))
    }, Date.parse(attempt.until) - Date.now())
    const timer = setInterval(() => {
      if (polling) return
      polling = true
      read().then((next) => settle(next, false), (err: unknown) => fail(err, false)).finally(() => {
        polling = false
      })
    }, pollMs)
    return () => {
      live = false
      clearTimeout(expiry)
      clearInterval(timer)
    }
  }, [attempt, read, client])

  const start = async (forApp: App, { replaces, at }: { replaces?: Connector; at?: string } = {}) => {
    setMintError(null)
    setPollError(null)
    setConnected(null)
    setExpired(false)
    const shown = connectors
    const { connectors: fresh, ...rest } = await client.connectorList()
    setInfo(rest)
    setConnectors(fresh)
    setListError(null)
    // A connector added at the server's own URL reconnects there, where the
    // page asks for a code even when the universal URL takes the sign-in.
    const elsewhere = at && at !== rest.url ? at : undefined
    const signIn = rest.sign_in && !elsewhere
    const code = signIn ? null : await client.connectorCode()
    // Signing in needs nothing from here, so it may be done already: what
    // the page showed before is what counts as already there.
    const known = new Set((signIn && shown ? shown : fresh).map((c) => c.id))
    setApp(forApp)
    setAttempt({
      id: crypto.randomUUID(),
      code,
      until: code?.expires_at ?? new Date(Date.now() + watchMs).toISOString(),
      app: forApp,
      known,
      at: elsewhere,
      replaces,
      into: rest.universal_url,
    })
  }

  const mint = async () => {
    setBusy(true)
    try {
      await start(app, { replaces: attempt?.replaces, at: attempt?.at })
    } catch (err) {
      setMintError(failure(err))
    } finally {
      setBusy(false)
    }
  }

  // A connector added at a URL other than the one shown reconnects there, with a code.
  const signsIn = (c: Connector) => !!info.sign_in && (!c.resource || c.resource === info.url)
  const tooOld = listError?.old === true
  const edgeNote = edge && !edge.edge_url
    ? <p className="text-muted">This server has no edge, so ChatGPT and Claude cannot reach it. An admin turns one on: <Link href={`${docs}/edge.md#turning-it-on`}>Edge guide</Link>.</p>
    : edgeError && <p className="text-muted">The URL appears with the code. Reading the server's edge failed: {edgeError}</p>
  const offline = edge?.edge_url && !edge.connected
    ? <p className="text-muted">This server is not connected to its edge at {edge.edge_url}; an admin sees why with <Code>sudo aether-server edge status</Code>.</p>
    : null

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
                <Setup app={value} info={info} edgeNote={edgeNote} code={attempt?.code ?? null} at={attempt?.at} watching={attempt !== null} busy={busy} expired={expired} onMint={() => void mint()} />
              </TabsContent>
            ))}
            {offline}
          </Tabs>
        )}
        <div role="status" className="flex min-w-0 flex-col gap-2 text-ui-sm empty:hidden">
          {attempt && (
            <p className="flex items-center gap-1.5 text-muted">
              <Spinner />
              {attempt.replaces && connected
                ? `Waiting for ${label(connected)} to call Aether, then revoking ${label(attempt.replaces)}…`
                : `Waiting for ${attempt.app === 'chatgpt' ? 'ChatGPT' : 'Claude'} to connect…`}
            </p>
          )}
          {connected && (
            <Callout
              tone="done"
              title={`${label(connected)} connected`}
              actions={<Button ref={done} size="sm" variant="secondary" onClick={() => setConnected(null)}>Done</Button>}
            >
              With {scopeList(connected)}. Ask it “What needs me in Aether?”
            </Callout>
          )}
        </div>
        {pollError && <FailureCallout failure={pollError} />}
        {mintError && <FailureCallout failure={mintError} />}
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
      {connectors?.map((c) => {
        const movable = !!c.resource && !!info.universal_url && c.resource !== info.universal_url && info.url === info.universal_url
        return (
          <SettingRow
            key={c.id}
            label={label(c)}
            help={(
              <>
                <span className="block">{scopeList(c)}</span>
                {c.resource && <span className="block">{c.resource === info.universal_url ? 'Universal URL' : <>This server’s own URL, <Code>{c.resource}</Code></>}</span>}
                Added <RelativeTime at={c.created_at} />
                {' · '}{c.last_used_at ? <>last used <RelativeTime at={c.last_used_at} /></> : 'never used'}
              </>
            )}
            control={(
              <>
                <Button size="sm" variant="ghost" aria-label={`Reconnect ${label(c)}`} disabled={busy} onClick={() => setConfirming({ connector: c, action: 'reconnect' })}>Reconnect</Button>
                <Button size="sm" variant="ghost" aria-label={`Revoke ${label(c)}`} disabled={busy} onClick={() => setConfirming({ connector: c, action: 'revoke' })}>Revoke</Button>
              </>
            )}
          >
            {movable && <Button size="sm" variant="secondary" className="self-start" aria-label={`Move ${label(c)} to the universal URL`} disabled={busy} onClick={() => setConfirming({ connector: c, action: 'move' })}>Move to the universal URL</Button>}
          </SettingRow>
        )
      })}
      {confirming?.action === 'move' && (
        <Confirm
          title={`Move ${label(confirming.connector)} to the universal URL?`}
          description={`Add Aether again in ${confirming.connector.client_name} with ${info.universal_url}${info.sign_in ? ' and sign in' : ' and paste the code'}. Once the new connection calls Aether, this connector is revoked; remove the old Aether connector in ${confirming.connector.client_name} too.`}
          action="Move"
          pending="Starting…"
          onConfirm={async () => {
            setBusy(true)
            try {
              await start(appOf(confirming.connector), { replaces: confirming.connector })
            } finally {
              setBusy(false)
            }
          }}
          onDone={() => {}}
          onClose={() => setConfirming(null)}
        />
      )}
      {confirming && confirming.action !== 'move' && (confirming.action === 'reconnect' ? (
        <Confirm
          title={`Reconnect ${label(confirming.connector)}?`}
          description={signsIn(confirming.connector)
            ? `This revokes the connector. Connect Aether again in ${confirming.connector.client_name} and sign in; you can choose other scopes.`
            : `This gets a new code, then revokes the connector. Connect Aether again in ${confirming.connector.client_name} and paste the code; you can choose other scopes.`}
          action="Reconnect"
          pending="Reconnecting…"
          onConfirm={async () => {
            setBusy(true)
            try {
              await start(appOf(confirming.connector), { at: confirming.connector.resource })
              await client.connectorRevoke(confirming.connector.id)
            } finally {
              setBusy(false)
              void refresh()
            }
          }}
          onDone={() => {}}
          onClose={() => setConfirming(null)}
        />
      ) : (
        <Confirm
          title={`Revoke ${label(confirming.connector)}?`}
          description={`Its next call fails, and ${confirming.connector.client_name} asks you to connect again${signsIn(confirming.connector) ? '' : ' with a new code'}. Removing it in ${confirming.connector.client_name} does not revoke it here.`}
          action="Revoke"
          pending="Revoking…"
          onConfirm={async () => {
            await client.connectorRevoke(confirming.connector.id)
            setConnectors((list) => list && list.filter((c) => c.id !== confirming.connector.id))
          }}
          onDone={() => {
            toast.success(`${label(confirming.connector)} revoked`)
            void refresh()
          }}
          onClose={() => setConfirming(null)}
        />
      ))}
    </SettingsSection>
  )
}
