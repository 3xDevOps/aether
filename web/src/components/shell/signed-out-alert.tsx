import { useEffect } from 'react'
import { signedOutTitle } from '@/components/agents/agent-copy'
import { Button } from '@/components/ui/button'
import { toneClasses } from '@/components/ui/status-dot'
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
  // The Agents page and a session whose turn was refused show the finding beside their own sign-in action.
  const shownInPlace = useStore((s) => {
    if (s.route.name === 'agents') return true
    const reason = s.route.name === 'run' ? s.runs[s.route.params.runId ?? '']?.reason : undefined
    return reason !== undefined && refusable.test(reason) && reason.includes('Authentication required')
  })
  const signedOut = agents?.filter((agent) => agent.installed && agent.login_problem) ?? []
  if (!signedOut.length || shownInPlace) return null
  const { text, soft } = toneClasses('needs-you')
  return (
    <div role="alert" data-tone="needs-you" className={cn('flex flex-col gap-x-3 gap-y-1 border-b border-seam px-3 py-1.5 text-ui text-text md:flex-row md:items-center', soft)}>
      <ul className="flex min-w-0 flex-1 flex-col gap-0.5">
        {signedOut.map((agent) => (
          <li key={agent.name} className="flex min-w-0 flex-col gap-x-3 md:flex-row md:items-baseline">
            <span className={cn('shrink-0 font-medium', text)}>{signedOutTitle(agent)}</span>
            <span className="min-w-0 font-code text-ui-sm break-words">{agent.login_problem}</span>
          </li>
        ))}
      </ul>
      <Button size="sm" variant="secondary" className="self-start md:self-center" onClick={() => navigate('agents')}>Sign in on Agents</Button>
    </div>
  )
}
