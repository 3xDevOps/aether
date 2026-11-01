import { useRef, useState, type ReactNode } from 'react'
import { label } from '@/components/agents/agent-copy'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Callout } from '@/components/ui/callout'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import type { AgentInfo } from '@/lib/types'

const Code = ({ children }: { children: ReactNode }) => <span className="font-code text-text">{children}</span>
const Name = ({ children }: { children: ReactNode }) => <strong className="font-medium text-text">{children}</strong>

export function RemoveAgent({ agent, client, onRemoved, focusAfterRemoval, onClose }: { agent: AgentInfo; client: Api; onRemoved: () => void; focusAfterRemoval: () => void; onClose: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const returnFocus = useReturnFocus()
  const removed = useRef(false)
  const name = label(agent)
  const executable = agent.executable ?? agent.name
  return (
    <AlertDialog open onOpenChange={() => !busy && onClose()}>
      <AlertDialogContent
        {...returnFocus}
        // After a removal the opener may be gone, so the page picks where focus goes.
        onCloseAutoFocus={(event) => {
          if (!removed.current) return returnFocus.onCloseAutoFocus(event)
          event.preventDefault()
          focusAfterRemoval()
        }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle className="break-words">Remove {name}?</AlertDialogTitle>
          <AlertDialogDescription className="break-words">
            {agent.source === 'member' ? (
              <>
                Deletes your <Code>{agent.name}</Code> definition. New sessions can't start it. <Code>~/.local/bin/{executable}</Code> and running sessions are untouched.{' '}
                <Name>Add agent…</Name> registers it again.
              </>
            ) : (
              <>
                Deletes <Code>~/.local/bin/{executable}</Code>
                {agent.enhanced_installed === true && agent.enhanced === 'adapter' && " and its Enhanced adapter's link"} from your environment. New sessions can't start it; running sessions keep going.
                Its login, settings and package files stay. <Name>Set up</Name> installs it again.
              </>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error !== null && <Callout tone="failed" role="alert">{error}</Callout>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              setBusy(true)
              setError(null)
              client.agentRemove(agent.name).then(
                () => {
                  removed.current = true
                  onRemoved()
                  onClose()
                },
                (err: unknown) => {
                  setBusy(false)
                  setError(errorSentence(err))
                },
              )
            }}
          >
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
