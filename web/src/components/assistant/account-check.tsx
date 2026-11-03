import { useEffect, useState } from 'react'
import type { Api } from '@/lib/api'
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
  // A start records the account, so a check made before it reads again.
  const assistantState = useStore((s) => s.assistantStatus?.state)
  const [text, setText] = useState<string | null>(null)
  useEffect(() => {
    if (!supported) return
    let live = true
    setText(null)
    client.assistantAccount(agent).then(
      (account) => live && setText(accountSentence(account)),
      (err: unknown) => live && setText(`Can't compare with your sessions' account: ${errorSentence(err)}`),
    )
    return () => {
      live = false
    }
  }, [agent, client, supported, assistantState])
  if (!text) return null
  return <p role="status" className={cn('text-ui-sm break-words text-muted', className)}>{text}</p>
}
