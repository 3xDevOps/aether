import type { Terminal } from '@xterm/xterm'
import { type FormEvent, useEffect, useRef, useState } from 'react'
import { TerminalPane } from '@/components/terminal-pane'
import { Button, buttonVariants } from '@/components/ui/button'
import { ArrowLeft } from '@/components/icons'
import { Callout } from '@/components/ui/callout'
import { CopyButton } from '@/components/ui/copy-button'
import { Input } from '@/components/ui/input'
import { useXterm } from '@/components/xterm-host'
import type { Api } from '@/lib/api'
import type { AssistantAgent } from '@/lib/assistant-types'
import { lastLine, readSignInPrompt, type SignInPrompt } from '@/lib/sign-in-prompt'
import { type Attachment, connectAttach } from '@/routes/terminal/attach'

const vendor: Record<string, string> = { claude: 'Claude', codex: 'Codex' }

/**
 * Runs the agent's sign-in in the assistant's own container, so the login it writes is the assistant's alone.
 * It offers the URL and code the sign-in prints; the terminal shows from the start only with `terminal`, and once the sign-in ends.
 */
export function AssistantSignIn({ agent, client, onDone, onBack, terminal: terminalFirst = true }: { agent: AssistantAgent; client: Api; onDone: () => void; onBack?: () => void; terminal?: boolean }) {
  const attachment = useRef<Attachment | null>(null)
  const output = useRef<Uint8Array[]>([])
  const live = useRef<Terminal | null>(null)
  const [prompt, setPrompt] = useState<SignInPrompt>({})
  const [code, setCode] = useState('')
  const [refused, setRefused] = useState<string>()
  const [ended, setEnded] = useState(false)
  const [shown, setShown] = useState(terminalFirst)
  // A replay is a rendered screen, where a wrapped URL without its link
  // target cannot be read whole; one printed live can.
  const [liveURL, setLiveURL] = useState<string>()
  const [said, setSaid] = useState('')
  const [sent, setSent] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const codeText = useRef<HTMLElement>(null)
  const controller = useXterm({
    onData: (data) => attachment.current?.send(data),
    onResize: (cols, rows) => attachment.current?.resize(cols, rows),
  })
  const { terminal, geometry } = controller
  useEffect(() => {
    let wasAttached = false
    let decoder = new TextDecoder()
    let text = ''
    let liveText = ''
    const attached = connectAttach(() => client.assistantLoginSocket(agent.name), {
      onData: (chunk, kind) => {
        output.current.push(chunk)
        const decoded = decoder.decode(chunk, { stream: true })
        text += decoded
        if (kind === 'live') {
          liveText += decoded
          setLiveURL(readSignInPrompt(liveText, agent.name).url)
        }
        setPrompt(readSignInPrompt(text, agent.name))
        setSaid(lastLine(text))
        live.current?.write(chunk)
      },
      onAttached: () => {
        wasAttached = true
        output.current = []
        decoder = new TextDecoder()
        text = ''
        liveText = ''
        live.current?.reset()
        setLiveURL(undefined)
        setSaid('')
        setRefused(undefined)
        // A size measured before the attach was accepted was dropped.
        if (live.current) attached.resize(live.current.cols, live.current.rows)
      },
      // A reconnect after the sign-in finished while disconnected would
      // start another sign-in and delete the one just made.
      onState: (state) => {
        if (state === 'reconnecting' && wasAttached) {
          attached.close()
          setEnded(true)
        }
      },
      onRefused: (detail) => setRefused(detail),
      onWriteDenied: (detail) => setRefused(detail ?? 'Terminal input was denied'),
      // A reconnect after the sign-in exits would start another one.
      onExit: () => {
        attached.close()
        setEnded(true)
      },
      geometry,
      wantsWrite: () => true,
    })
    attachment.current = attached
    return () => {
      attached.close()
      attachment.current = null
    }
  }, [agent.name, client, geometry, attempt])
  useEffect(() => {
    if (!terminal) return
    for (const chunk of output.current) terminal.write(chunk)
    live.current = terminal
    return () => {
      live.current = null
    }
  }, [terminal])
  // What the sign-in printed last is its real outcome or error.
  const settled = ended || refused !== undefined
  const url = prompt.linked || prompt.url === liveURL ? prompt.url : undefined
  const forced = prompt.url !== undefined && url === undefined
  const name = vendor[agent.name] ?? agent.display_name
  const sendCode = (event: FormEvent) => {
    event.preventDefault()
    // Separate frames, as a paste and then Enter arrive from the terminal.
    attachment.current?.send(code.trim())
    attachment.current?.send('\r')
    setCode('')
    // Whether the code worked, the sign-in says only in its output.
    setShown(true)
    setSent(true)
  }
  const retry = () => {
    setPrompt({})
    setCode('')
    setRefused(undefined)
    setEnded(false)
    setSent(false)
    setAttempt((n) => n + 1)
  }
  return (
    <section aria-label={`Sign in to ${agent.display_name}`} className="flex flex-col gap-3">
      {onBack && (
        <div>
          <Button size="sm" variant="ghost" onClick={() => {
            // The sign-in outlives its socket, and while it runs no other agent's can start.
            if (!ended) attachment.current?.send('\x03')
            onBack()
          }}><ArrowLeft />Choose another agent</Button>
        </div>
      )}
      <p className="text-ui">
        Sign in to {agent.display_name} for the assistant. The sign-in runs in the assistant's own sandbox and is kept there, apart from your sessions, which cannot read or change it.
      </p>
      {refused && (
        <Callout tone="failed" role="alert" title="The sign-in could not start" actions={<Button size="sm" variant="secondary" onClick={retry}>Try again</Button>}>
          <code className="font-code break-words">{refused}</code>
        </Callout>
      )}
      {!settled && (url ? (
        <div className="flex flex-col items-start gap-3">
          <a className={buttonVariants({ size: 'sm' })} href={url} target="_blank" rel="noopener noreferrer">Open {name} sign-in</a>
          {prompt.code && (
            <p className="flex items-center gap-1 text-ui">
              Enter this code there: <code ref={codeText} className="select-all font-code font-medium">{prompt.code}</code>
              <CopyButton text={prompt.code} label="Copy code" source={codeText} />
            </p>
          )}
          {agent.name === 'claude' && (
            <form className="flex w-full items-end gap-2" onSubmit={sendCode}>
              <label className="flex min-w-0 flex-1 flex-col gap-1 text-ui-sm">
                Paste the code
                <Input value={code} autoComplete="off" spellCheck={false} onChange={(event) => setCode(event.target.value)} />
              </label>
              <Button variant="secondary" type="submit" disabled={!code.trim()}>Send code</Button>
            </form>
          )}
          {sent && <p className="text-ui-sm text-muted">Code sent. The terminal below shows whether {name} accepted it.</p>}
        </div>
      ) : (
        <p role="status" className="text-ui-sm text-muted">{prompt.url ? 'Open the link the sign-in printed in the terminal below.' : 'Starting the sign-in…'}</p>
      ))}
      <div className="flex gap-2">
        <Button size="sm" variant={ended ? 'primary' : 'secondary'} onClick={onDone}>{ended ? 'Done' : 'Close'}</Button>
        {!settled && !forced && <Button size="sm" variant="ghost" aria-pressed={shown} onClick={() => setShown(!shown)}>{shown ? 'Hide terminal' : 'Show terminal'}</Button>}
      </div>
      {/* The terminal draws on a canvas, so what the sign-in said last is also read out. */}
      {sent && <p className="sr-only" aria-live="polite">{said}</p>}
      {(shown || ended || forced || (refused !== undefined && said !== '')) && (
        // The fit addon sizes the terminal from this box, so its height must not follow the terminal's.
        <div className="h-[min(32rem,70dvh)] min-h-64">
          <TerminalPane controller={controller} />
        </div>
      )}
    </section>
  )
}
