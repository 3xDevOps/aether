import { useEffect } from 'react'
import { signedOutTitle } from '@/components/agents/agent-copy'
import { Button } from '@/components/ui/button'
import { toneClasses } from '@/components/ui/status-dot'
import { cn } from '@/lib/utils'
import { useAgentList } from '@/routes/agents/use-agents'
import { useStore } from '@/store'
import { isTerminal } from '@/store/runs'

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
  // The Agents page shows every finding beside its Log in, and a live
  // session's Chat shows its own agent's beside Sign in on Agents after a
  // refusal.
  const onAgents = useStore((s) => s.route.name === 'agents')
  const inChat = useStore((s) => {
    const run = s.route.name === 'run' ? s.runs[s.route.params.runId ?? ''] : undefined
    const refused = run?.reason !== undefined && !isTerminal(run.status) && refusable.test(run.reason) && run.reason.includes('Authentication required')
    const own = (run?.account_member_id || run?.member_id) === s.info?.member.id
    return refused && own && s.shownRunView?.runId === run?.id && s.shownRunView?.view === 'session' ? run?.harness : undefined
  })
  const signedOut = agents?.filter((agent) => agent.installed && agent.login_problem && agent.name !== inChat) ?? []
  if (!signedOut.length || onAgents) return null
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
