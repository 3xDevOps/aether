import { useEffect, useRef, useState } from 'react'
import { modeLabel } from '@/components/launch/modes'
import { ChevronLeft } from '@/components/icons'
import { FillProgress, PromptFill, PromptFooter, runIfReady, useEntranceOnChange, useTypedValues, type PrimaryAction, type Typed } from '@/components/templates/prompt-fill'
import { TemplateList } from '@/components/templates/template-list'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Skeleton } from '@/components/ui/skeleton'
import { api, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { Template } from '@/lib/types'
import { cn } from '@/lib/utils'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { useCapability } from '@/store/hooks'
import { readSchedules, readTemplates, unread, useLists } from '@/store/templates'

/**
 * The compact template library for the composer and the palette: the list, the selected template's fill-ins and a
 * footer whose primary action the caller supplies. It shows the workspace's cached list at once and refreshes it.
 */
export function TemplatePicker({
  workspaceID,
  client = api,
  action,
  onOpenTemplates,
}: {
  workspaceID: string
  client?: Api
  action: (template: Template, typed: Typed) => PrimaryAction
  onOpenTemplates?: () => void
}) {
  const phone = useIsMobile()
  const caps = useCapability()
  const { templates, schedules } = useLists((all) => all[workspaceID] ?? unread)
  const { agents } = useAgentList()
  const names = agentDisplayNames(agents)
  const [error, setError] = useState<string | null>(null)
  const [chosen, setChosen] = useState<string>()
  const [detail, setDetail] = useState(false)
  const [hint, setHint] = useState<string | null>(null)
  const values = useTypedValues()
  const body = useRef<HTMLDivElement>(null)
  const loading = useDelayed(templates === null)

  const read = () => {
    setError(null)
    readTemplates(client, workspaceID).catch((err: unknown) => setError(message(err)))
    if (caps.hasMethod('schedule.list')) readSchedules(client, workspaceID).catch(() => {})
  }
  useEffect(read, [client, workspaceID])

  const selected = templates?.find((t) => t.name === chosen) ?? templates?.[0]
  useEntranceOnChange(body, selected?.name ?? '')

  if (templates?.length === 0) {
    return (
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 p-4">
        <EmptyState
          title="No templates yet"
          className="py-0"
          action={onOpenTemplates && <Button variant="secondary" size="sm" onClick={onOpenTemplates}>Open Templates</Button>}
        >
          This workspace has no templates.
        </EmptyState>
        {error && <FailureCallout error={error} onRetry={read} />}
      </div>
    )
  }

  const typed = selected ? values.of(selected) : {}
  const primary = selected && action(selected, typed)
  const showList = !phone || !detail
  const showDetail = selected && (!phone || detail)
  return (
    <div className="@container flex min-h-0 flex-1 flex-col">
      <div className="flex min-h-0 flex-1 flex-col @min-[34rem]:flex-row">
        {showList && (
          <div className={cn('flex min-h-0 flex-col gap-2 p-2', !phone && 'border-seam @max-[34rem]:max-h-44 @max-[34rem]:border-b @min-[34rem]:w-56 @min-[34rem]:shrink-0 @min-[34rem]:border-r', phone && 'flex-1')}>
            {error && <FailureCallout error={error} onRetry={read} />}
            {templates === null ? (
              loading && !error && (
                <div aria-label="Loading templates" className="flex flex-col gap-1">
                  {[0, 1, 2].map((i) => <Skeleton key={i} className="h-8" />)}
                </div>
              )
            ) : (
              <TemplateList
                templates={templates}
                schedules={schedules}
                agentNames={names}
                selected={phone ? undefined : selected?.name}
                autoFocus={!phone}
                raised
                onSelect={(name) => {
                  setChosen(name)
                  setDetail(true)
                }}
              />
            )}
          </div>
        )}
        {showDetail && (
          <div className="min-h-0 flex-1 overflow-y-auto">
            <div ref={body} className="flex flex-col gap-3 px-4 py-3">
              {phone && (
                <Button variant="ghost" size="sm" className="-ml-2 self-start" onClick={() => setDetail(false)}>
                  <ChevronLeft />Templates
                </Button>
              )}
              <div className="flex min-w-0 flex-col gap-1">
                <div className="flex min-w-0 items-center gap-2">
                  <h3 className="min-w-0 truncate font-medium text-text">{selected.name}</h3>
                  <Badge className="h-4.5 text-ui-xs">{modeLabel(selected.mode)}</Badge>
                  <span className="ml-auto"><FillProgress template={selected} typed={typed} /></span>
                </div>
                <p className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
                  <AgentGlyph agent={selected.harness} />
                  <span className="truncate">{names[selected.harness] ?? selected.harness} · {selected.model || 'agent default'}</span>
                </p>
              </div>
              <PromptFill
                template={selected}
                typed={typed}
                onValue={(name, value) => values.set(selected, name, value)}
                onHint={setHint}
                onComplete={() => primary && runIfReady(primary)}
              />
            </div>
          </div>
        )}
      </div>
      {selected && primary && (!phone || detail) && <PromptFooter template={selected} typed={typed} hint={hint} primary={primary} className="px-3" />}
    </div>
  )
}
