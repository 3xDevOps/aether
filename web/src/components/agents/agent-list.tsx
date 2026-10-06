import { type ReactNode, useEffect, useRef } from 'react'
import { enhancedSupported, label, ready, supportWords } from '@/components/agents/agent-copy'
import { Ellipsis } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { StatusDot } from '@/components/ui/status-dot'
import type { AgentInfo } from '@/lib/types'

function facts(agent: AgentInfo): string {
  if (agent.installed !== true) return 'Not installed'
  if (agent.login_found === undefined) return 'Installed'
  return agent.login_found ? 'Installed · Login found' : 'Installed · No login found'
}

export function AgentList({
  agents,
  label: listLabel = 'Agents',
  onSetUp,
  onRun,
  extra,
  returnFocusTo,
  primary,
}: {
  agents: AgentInfo[]
  label?: string
  onSetUp?: (agent: AgentInfo) => void
  onRun?: (agent: AgentInfo) => void
  extra?: (agent: AgentInfo) => ReactNode
  returnFocusTo?: string
  primary?: string
}) {
  const list = useRef<HTMLUListElement>(null)
  useEffect(() => {
    if (!returnFocusTo) return
    const actions = list.current?.querySelectorAll<HTMLElement>('[data-agent-action]') ?? []
    Array.from(actions).find((action) => action.dataset.agentAction === returnFocusTo)?.focus()
  }, [returnFocusTo])
  return (
    <ul ref={list} aria-label={listLabel} className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
      {agents.map((agent) => {
        const name = label(agent)
        const runnable = ready(agent) && onRun
        const action = runnable ? 'Run' : agent.installed === true ? 'Log in' : 'Set up'
        return (
          <li key={agent.name} className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5">
            <AgentGlyph agent={agent.glyph ?? agent.name} colored className="size-5" />
            <span className="flex min-w-[60%] flex-1 flex-col">
              <span className="truncate text-ui font-medium text-text">{name}</span>
              <span className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-ui-sm text-muted">
                <StatusDot tone={ready(agent) ? 'done' : 'neutral'} />
                <span>{facts(agent)}</span>
                <span aria-hidden>·</span>
                <span aria-label={enhancedSupported(agent) ? 'Supports Standard and Enhanced' : 'Supports Standard only'}>{supportWords(agent)}</span>
              </span>
            </span>
            <span className="ml-auto flex items-center gap-2">
              {extra?.(agent)}
              {(runnable || onSetUp) && (
                <Button
                  size="sm"
                  variant={agent.name === primary ? 'primary' : 'secondary'}
                  data-agent-action={agent.name}
                  aria-label={`${action} ${name}`}
                  onClick={() => (runnable ? onRun(agent) : onSetUp?.(agent))}
                >
                  {action}
                </Button>
              )}
              {runnable && onSetUp && (
                <Menu>
                  <MenuTrigger asChild>
                    <Button size="icon" variant="ghost" label={`More for ${name}`}>
                      <Ellipsis />
                    </Button>
                  </MenuTrigger>
                  <MenuContent align="end">
                    <MenuItem onSelect={() => onSetUp(agent)}>Set up again</MenuItem>
                  </MenuContent>
                </Menu>
              )}
            </span>
          </li>
        )
      })}
    </ul>
  )
}
