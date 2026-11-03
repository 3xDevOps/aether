import { useEffect, useRef, useState } from 'react'
import { ApiError, type Api, codeMethodNotFound } from '@/lib/api'
import type { AssistantAccount } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const vendor: Record<string, string> = { claude: 'Claude', codex: 'Codex' }

export function accountSentence(account: AssistantAccount): string | null {
  const name = vendor[account.agent] ?? account.agent
  const email = account.email ? ` (${account.email})` : ''
  switch (account.state) {
    case 'same':
      return `Same ${name} account as your sessions${email}`
    case 'other':
      return `Your sessions use another ${name} account (${[account.environment, account.email].filter(Boolean).join(', ')})`
    case 'expired':
      return `Can't compare: your environment's ${name} token is expired. Start a session to refresh it`
    case 'error':
      return `Can't compare with your sessions' ${name} account: ${account.error}`
    default:
      return null
  }
}

/** Whether the assistant's sign-in names the account your sessions use. Information only: it reads the environment's login and changes nothing. */
export function AssistantAccountCheck({ agent, client, className }: { agent: string; client: Api; className?: string }) {
  const supported = useCapability().hasMethod('assistant.account')
  const assistantState = useStore((s) => s.assistantStatus?.state)
  // A start records the account, so only a check made before it reads
  // again; one that found it keeps the state it was made in.
  const found = useRef<{ state: typeof assistantState } | null>(null)
  const retry = found.current ? found.current.state : assistantState
  const [text, setText] = useState<string | null>(null)
  useEffect(() => {
    if (!supported) return
    let live = true
    setText(null)
    client.assistantAccount(agent).then(
      (account) => {
        if (!live) return
        if (account.state !== 'unrecorded') found.current = { state: retry }
        setText(accountSentence(account))
      },
      // A server behind a gateway that lists every method may not have it.
      (err: unknown) => live && setText(err instanceof ApiError && err.code === codeMethodNotFound ? null : `Can't compare with your sessions' account: ${errorSentence(err)}`),
    )
    return () => {
      live = false
    }
  }, [agent, client, supported, retry])
  if (!text) return null
  return <p role="status" className={cn('text-ui-sm break-words text-muted', className)}>{text}</p>
}
