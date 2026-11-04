import { type ReactNode, useEffect, useRef, useState } from 'react'
import { ApiError, type Api, codeMethodNotFound } from '@/lib/api'
import type { AssistantAccount } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const vendor: Record<string, string> = { claude: 'Claude', codex: 'Codex' }

export function accountSentence(account: AssistantAccount, where: 'panel' | 'settings'): ReactNode {
  const name = vendor[account.agent] ?? account.agent
  const email = account.email ? ` (${account.email})` : ''
  switch (account.state) {
    case 'same':
      return <><span className="font-medium text-text">Same {name} account</span> as your sessions{email}.</>
    case 'other':
      return (
        <>
          <span className="font-medium text-text">Different {name} account:</span> the assistant does not use the account your sessions use.
          {' '}Your sessions: {account.email ? `${account.email}, ` : ''}<code className="font-code">{account.environment}</code>.
          {where === 'panel' && account.environment !== 'an API key' && ` To switch, select Sign in again in Settings > Assistant and sign in ${account.email ? `as ${account.email}` : "with your sessions' account"}.`}
        </>
      )
    case 'expired':
      return `Can't compare: your environment's ${name} token has expired. Start a session to refresh it.`
    case 'error':
      return <>Can't compare with your sessions' {name} account: <code className="font-code">{account.error}</code></>
    default:
      return null
  }
}

/** Whether the assistant's sign-in names the account your sessions use. Information only: it reads the environment's login and changes nothing. */
export function AssistantAccountCheck({ agent, client, className, where = 'settings' }: { agent: string; client: Api; className?: string; where?: 'panel' | 'settings' }) {
  const supported = useCapability().hasMethod('assistant.account')
  const assistantState = useStore((s) => s.assistantStatus?.state)
  // A start records the account and a session refreshes an expired token,
  // so only a check that compared nothing reads again.
  const found = useRef<{ state: typeof assistantState } | null>(null)
  const retry = found.current ? found.current.state : assistantState
  const [text, setText] = useState<ReactNode>(null)
  useEffect(() => {
    if (!supported) return
    let live = true
    setText(null)
    client.assistantAccount(agent).then(
      (account) => {
        if (!live) return
        if (account.state === 'same' || account.state === 'other') found.current = { state: retry }
        setText(accountSentence(account, where))
      },
      // A server behind a gateway that lists every method may not have it.
      (err: unknown) => live && setText(err instanceof ApiError && err.code === codeMethodNotFound ? null : <>Can't compare with your sessions' account: <code className="font-code">{errorSentence(err)}</code></>),
    )
    return () => {
      live = false
    }
  }, [agent, client, supported, retry, where])
  if (!text) return null
  return <p role="status" className={cn('text-ui-sm break-words text-muted', className)}>{text}</p>
}
