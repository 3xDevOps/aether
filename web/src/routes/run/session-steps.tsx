import { useEffect, useMemo, useState } from 'react'
import { Bot, Brain, FileText, Globe, ListTodo, Search, Wrench, type LucideIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Markdown } from '@/components/ui/markdown'
import { DiffBlock, OutputTail, PlanEntries, ToolValue } from '@/components/ui/session-blocks'
import { Spinner } from '@/components/ui/spinner'
import { StatusDot } from '@/components/ui/status-dot'
import { IconStack, ProseMark, StepChip, StepChipToggle, StepLine, StepRow, StepsHeader, StepsLiveMark, StepStatus } from '@/components/ui/steps'
import { TextSwap } from '@/components/ui/text-swap'
import { duration, toolIcon } from '@/components/ui/timeline'
import { api } from '@/lib/api'
import { useSecondClock } from '@/lib/clock'
import { message } from '@/lib/format'
import { openInFiles } from '@/routes/files/open'
import { PathText, useFileCheckout } from '@/routes/files/path-link'
import { parsePath } from '@/routes/files/paths'
import { answerTone } from '@/routes/run/request-options'
import { AnsweredText } from '@/routes/run/session-requests'
import { SubagentGroup } from '@/routes/run/subagent-cards'
import { useStore } from '@/store'
import { workSummary, type Step, type StepsRow, type WorkItem } from '@/store/session-steps'
import { loadWholeItem } from '@/store/session-stream'

const chipsShown = 4

function useOpen(runID: string, key: string, fallback = false): [boolean, () => void] {
  const open = useStore((s) => s.expandedRows[runID]?.[key] ?? fallback)
  return [open, () => useStore.getState().setSessionRow(runID, key, !open)]
}

function iconOf(tool: string): LucideIcon {
  return toolIcon[tool.toLowerCase()] ?? Wrench
}

function clock(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`
}

function Elapsed({ since }: { since: string }) {
  useSecondClock(true)
  return <>{clock(Date.now() - Date.parse(since))}</>
}

export function hasDetail(entry: WorkItem): boolean {
  return Boolean(entry.command || entry.output || entry.diffs || entry.truncated || entry.input !== undefined || entry.result !== undefined)
}

export function StepsBlockHeader({ row, open, runID }: { row: StepsRow; open: boolean; runID: string }) {
  const { live } = row
  const label = live ? live.label : row.summary
  const counts = [
    row.failed > 0 && `${row.failed} failed`,
    row.plan && `plan ${row.plan.done} of ${row.plan.total} done`,
    !live && row.running > 0 && `${row.running} running`,
  ].filter(Boolean)
  return (
    <StepsHeader
      name={[label, live && row.summary, ...counts].filter(Boolean).join(', ')}
      open={open}
      onToggle={() => useStore.getState().setSessionRow(runID, row.id, !open)}
      lead={live ? <StepsLiveMark tone={live.tone} /> : <IconStack icons={row.icons.map(iconOf)} />}
      label={(
        <TextSwap className="grid min-w-0" layerClassName={live ? (live.tone === 'working' ? 'truncate live-shimmer' : 'truncate text-text') : 'truncate'}>
          {label}
        </TextSwap>
      )}
      meta={(
        <>
          {row.failed > 0 && <span className="text-state-failed">· {row.failed} failed</span>}
          {row.plan && <span>· Plan {row.plan.done}/{row.plan.total}</span>}
          {!live && row.running > 0 && <span>· {row.running} running</span>}
          {live && <span className="text-icon-faint">· <Elapsed since={row.start} /></span>}
        </>
      )}
    />
  )
}

function ToolLabel({ entry }: { entry: WorkItem }) {
  const counts = useMemo(() => {
    if (!entry.diffs) return null
    let add = 0
    let del = 0
    for (const diff of entry.diffs) {
      for (const line of diff.patch.split('\n')) {
        if (line.startsWith('+') && !line.startsWith('+++')) add++
        else if (line.startsWith('-') && !line.startsWith('---')) del++
      }
    }
    return { add, del }
  }, [entry.diffs])
  if (entry.command && entry.label.endsWith(entry.command)) {
    return <>{entry.label.slice(0, -entry.command.length)}<span className="font-code text-ui-sm text-text">{entry.command}</span></>
  }
  if (entry.name) return <><span className="font-code text-ui-sm text-text">{entry.name}</span> {entry.target}</>
  return (
    <>
      {entry.label}
      {counts && <span className="font-code text-ui-sm tabular-nums"> <span className="text-diff-add">+{counts.add}</span> <span className="text-diff-del">−{counts.del}</span></span>}
    </>
  )
}

function ToolStep({ id, entry, first, last, live, runID }: { id: string; entry: WorkItem; first: boolean; last: boolean; live: boolean; runID: string }) {
  const [open, toggle] = useOpen(runID, id)
  const detail = hasDetail(entry)
  const Icon = iconOf(entry.tool)
  const time = entry.status === 'running'
    ? live ? <Elapsed since={entry.at} /> : null
    : entry.status === 'failed'
      ? <span className="text-state-failed">{entry.exitCode ? `Exit ${entry.exitCode}` : 'Failed'}</span>
      : duration(entry.durationMs)
  return (
    <StepRow mark={<Icon />} tall={detail} first={first} last={last}>
      <StepLine expanded={detail ? open : undefined} onToggle={detail ? toggle : undefined} trailing={<StepStatus status={entry.status} waiting={entry.waiting}>{time}</StepStatus>}>
        <ToolLabel entry={entry} />
      </StepLine>
    </StepRow>
  )
}

function basename(path: string): string {
  return path.split('/').filter(Boolean).at(-1) ?? path
}

function host(url: string): string | undefined {
  try {
    const parsed = new URL(url)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.host : undefined
  } catch {
    return undefined
  }
}

function LookupChip({ entry }: { entry: WorkItem }) {
  const checkout = useFileCheckout()
  const target = entry.target!
  if (entry.tool === 'read') {
    const file = parsePath(target)
    return <StepChip icon={FileText} tip={target} onOpen={checkout && file ? () => openInFiles(checkout.id, file) : undefined}>{basename(target)}</StepChip>
  }
  const site = entry.tool === 'fetch' ? host(target) : undefined
  if (site) return <StepChip icon={Globe} tip={target} href={target}>{site}</StepChip>
  return <StepChip icon={entry.tool === 'fetch' ? Globe : Search} tip={target}>{target}</StepChip>
}

function LookupStep({ id, entries, first, last, live, runID }: { id: string; entries: WorkItem[]; first: boolean; last: boolean; live: boolean; runID: string }) {
  const [all, toggle] = useOpen(runID, `${id}/chips`)
  const running = [...entries].reverse().find((entry) => entry.status === 'running')
  const failed = entries.filter((entry) => entry.status === 'failed').length
  const Icon = iconOf(entries[0]!.tool)
  const shown = all ? entries : entries.slice(0, chipsShown)
  return (
    <StepRow mark={<Icon />} first={first} last={last}>
      <StepLine trailing={running ? <Spinner label="Running" className="text-state-working" /> : failed > 0 ? <span className="shrink-0 text-ui-sm text-state-failed">{failed} failed</span> : undefined}>
        <span className={running && live ? 'live-shimmer' : undefined}>{running ? running.label : workSummary(entries.map((entry) => entry.tool))}</span>
      </StepLine>
      <div className="flex flex-wrap gap-1.5 pb-2">
        {shown.map((entry) => <LookupChip key={entry.id} entry={entry} />)}
        {entries.length > chipsShown && <StepChipToggle open={all} hidden={entries.length - chipsShown} onToggle={toggle} />}
      </div>
    </StepRow>
  )
}

const previewChars = 280

function ThoughtStep({ id, text, streaming, first, last, live, runID }: { id: string; text: string; streaming: boolean; first: boolean; last: boolean; live: boolean; runID: string }) {
  const [open, toggle] = useOpen(runID, id)
  const flat = text.replace(/\s+/g, ' ').trim()
  const preview = streaming && flat.length > previewChars ? `…${flat.slice(-previewChars)}` : flat
  return (
    <StepRow mark={<Brain />} tall first={first} last={last}>
      <StepLine expanded={open} onToggle={toggle}>
        {streaming ? <span className={live ? 'live-shimmer' : undefined}>Thinking</span> : 'Thought'}
      </StepLine>
      <div className="pb-2">
        {open ? <Markdown text={text} muted live={streaming} /> : <p className="line-clamp-2 text-ui-sm break-words text-muted">{preview}</p>}
      </div>
    </StepRow>
  )
}

export function MoreSteps({ id, hidden, open, runID }: { id: string; hidden: number; open: boolean; runID: string }) {
  return (
    <StepRow mark={<span className="size-1 rounded-full bg-icon-faint" />}>
      <Button variant="quiet" className="h-7 text-ui" aria-expanded={open} onClick={() => useStore.getState().setSessionRow(runID, id, !open)}>
        {open ? 'Show fewer steps' : `Show ${hidden} more steps`}
      </Button>
    </StepRow>
  )
}

export function StepView({ step, first, last, live, runID }: { step: Step; first: boolean; last: boolean; live: boolean; runID: string }) {
  switch (step.kind) {
    case 'tool':
      return <ToolStep id={step.id} entry={step.entry} first={first} last={last} live={live} runID={runID} />
    case 'lookup':
      return <LookupStep id={step.id} entries={step.entries} first={first} last={last} live={live} runID={runID} />
    case 'thought':
      return <ThoughtStep id={step.id} text={step.text} streaming={step.streaming} first={first} last={last} live={live} runID={runID} />
    case 'narration':
      return (
        <StepRow mark={<ProseMark />} first={first} last={last}>
          <div className="pt-[3px] pb-2"><Markdown text={step.text} live={step.streaming} /></div>
        </StepRow>
      )
    case 'plan': {
      const done = step.entries.filter((entry) => entry.status === 'completed').length
      return (
        <StepRow mark={<ListTodo />} first={first} last={last}>
          <StepLine>Plan <span className="tabular-nums">{done}/{step.entries.length}</span></StepLine>
          <PlanEntries entries={step.entries} live={live} />
        </StepRow>
      )
    }
    case 'subagents':
      return (
        <StepRow mark={<Bot />} first={first} last={last}>
          <div className="pb-1"><SubagentGroup runID={runID} rowID={step.id} ids={step.ids} /></div>
        </StepRow>
      )
    case 'answered':
      return (
        <StepRow mark={<StatusDot tone={answerTone(step.request)} />} first={first} last={last}>
          <StepLine className="text-text"><AnsweredText request={step.request} command={step.command} /></StepLine>
        </StepRow>
      )
    case 'event':
      return (
        <StepRow mark={<StatusDot tone={step.tone ?? 'neutral'} />} first={first} last={last}>
          <StepLine className={step.tone && step.tone !== 'neutral' ? 'text-text' : undefined}>{step.text}</StepLine>
          {step.detail && <p className="line-clamp-2 pb-2 text-ui-sm break-words whitespace-pre-wrap text-muted">{step.detail}</p>}
        </StepRow>
      )
  }
}

export function StepDetail({ entry, last, runID }: { entry: WorkItem; last: boolean; runID: string }) {
  const [error, setError] = useState<string>()
  const missing = entry.truncated && entry.seq !== undefined
  useEffect(() => {
    if (!missing) return
    loadWholeItem(useStore, api, runID, entry.seq!).catch((err) => setError(message(err)))
  }, [missing, runID, entry.seq])
  const empty = !entry.command && !entry.output && !entry.diffs && entry.input === undefined && entry.result === undefined
  return (
    <StepRow last={last}>
      <div data-slot="step-detail" className="flex flex-col gap-2 pb-2">
        {entry.command && <code className="font-code text-ui-sm break-all text-text">$ <PathText>{entry.command}</PathText></code>}
        {entry.output && <OutputTail output={entry.output} exitCode={entry.exitCode} />}
        {entry.diffs?.map((diff) => <DiffBlock key={diff.path} path={diff.path} patch={diff.patch} />)}
        {entry.input !== undefined && <ToolValue label="Input" value={entry.input} />}
        {entry.result !== undefined && <ToolValue label="Result" value={entry.result} failed={entry.status === 'failed'} />}
        {missing && !error && <p className="text-ui-sm text-muted">Loading the full output…</p>}
        {error && <p role="alert" className="text-ui-sm text-state-failed">The full output could not be read: {error}</p>}
        {empty && !missing && <p className="text-ui-sm text-muted">No output.</p>}
      </div>
    </StepRow>
  )
}
