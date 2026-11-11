import { useEffect } from 'react'
import { label } from '@/components/agents/agent-copy'
import { verbatim } from '@/components/update-banner-shared'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { cn } from '@/lib/utils'
import { useAgentList } from '@/routes/agents/use-agents'
import { useStore } from '@/store'

// Mirrors acpTurnFailedReason and acpFailedReason in internal/scheduler/acp_turn.go.
const refusable = /^enhanced (turn|session) failed: /
const rereadEvery = 60_000

/** agent.list carries no event, so a signed-out login is noticed by re-reading it. */
function useLoginWatch(reload: () => void) {
  useEffect(() => {
    const tick = window.setInterval(() => {
      if (document.visibilityState === 'visible') reload()
    }, rereadEvery)
    window.addEventListener('focus', reload)
    const unsubscribe = useStore.subscribe((state, prev) => {
      if (state.runs === prev.runs) return
      const me = state.info?.member.id
      const failed = Object.values(state.runs).some((run) =>
        run.member_id === me && run.reason !== undefined && refusable.test(run.reason) && prev.runs[run.id]?.reason !== run.reason)
      if (failed) reload()
    })
    return () => {
      window.clearInterval(tick)
      window.removeEventListener('focus', reload)
      unsubscribe()
    }
  }, [reload])
}

export function SignedOutAlert() {
  const { agents, reload } = useAgentList()
  useLoginWatch(reload)
  const navigate = useStore((s) => s.navigate)
  const signedOut = agents?.filter((agent) => agent.installed && agent.login_problem) ?? []
  if (!signedOut.length) return null
  const names = signedOut.map(label)
  const title = names.length === 1 ? `${names[0]} is signed out` : `${names.slice(0, -1).join(', ')} and ${names.at(-1)} are signed out`
  return (
    <div className="border-b border-seam bg-chrome px-3 py-2">
      <Callout
        tone="needs-you"
        role="alert"
        title={title}
        actions={<Button size="sm" onClick={() => navigate('agents')}>Sign in on Agents</Button>}
      >
        <ul className="flex flex-col gap-1">
          {signedOut.map((agent) => (
            <li key={agent.name} className={cn(verbatim, 'border-state-needs-you/30 bg-transparent')}>
              {signedOut.length > 1 && `${label(agent)}: `}{agent.login_problem}
            </li>
          ))}
        </ul>
      </Callout>
    </div>
  )
}
