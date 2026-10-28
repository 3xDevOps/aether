import { useEffect, useRef, useState } from 'react'
import { TerminalPane } from '@/components/terminal-pane'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { useXterm } from '@/components/xterm-host'
import type { Api } from '@/lib/api'
import type { AssistantAgent } from '@/lib/assistant-types'
import { type Attachment, connectAttach } from '@/routes/terminal/attach'

/** Runs the agent's sign-in in the assistant's own container, so the login it writes is the assistant's alone. */
export function AssistantSignIn({ agent, client, onDone }: { agent: AssistantAgent; client: Api; onDone: () => void }) {
  const attachment = useRef<Attachment | null>(null)
  const [refused, setRefused] = useState<string>()
  const [ended, setEnded] = useState(false)
  const controller = useXterm({
    onData: (data) => attachment.current?.send(data),
    onResize: (cols, rows) => attachment.current?.resize(cols, rows),
  })
  const { terminal, geometry } = controller
  useEffect(() => {
    if (!terminal) return
    const attached = connectAttach(() => client.assistantLoginSocket(agent.name), {
      onData: (chunk) => terminal.write(chunk),
      onAttached: () => {
        terminal.reset()
        setRefused(undefined)
      },
      // A reconnect after the sign-in finished while disconnected would
      // start another sign-in and delete the one just made.
      onState: (state) => {
        if (state === 'reconnecting') {
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
  }, [agent.name, client, geometry, terminal])
  return (
    <section aria-label={`Sign in to ${agent.display_name}`} className="flex min-h-0 flex-1 flex-col gap-3">
      <p className="text-ui">
        Sign in to {agent.display_name} for the assistant. The sign-in runs in the assistant's own container and is kept there, apart from your runs, which cannot read or change it. Open the link it prints and follow its prompts.
      </p>
      {refused && <Callout tone="failed" role="alert" title="The sign-in could not start">{refused}</Callout>}
      <TerminalPane controller={controller} className="min-h-64 flex-1" />
      <div>
        <Button size="sm" variant={ended ? 'primary' : 'secondary'} onClick={onDone}>{ended ? 'Done' : 'Close'}</Button>
      </div>
    </section>
  )
}
