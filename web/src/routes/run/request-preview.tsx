import { useEffect, useMemo, useState } from 'react'
import type * as React from 'react'
import { Copy, ExternalLink, FileText, Globe } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { CodeBlock } from '@/components/ui/code'
import { Markdown } from '@/components/ui/markdown'
import { Clamped } from '@/components/ui/request-card'
import { RequestRow, RequestRows, RequestSection } from '@/components/ui/request-section'
import { DiffLines, patchFile } from '@/components/ui/session-blocks'
import { api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { Counts } from '@/routes/diff/patch-view'
import type { SessionItem, SessionRequest, SessionToolCall } from '@/lib/session-types'
import { callPaths } from '@/routes/run/request-options'
import { useStore } from '@/store'
import { loadWholeItem } from '@/store/session-stream'

const fileKinds = ['edit', 'delete', 'move', 'read']

function hasSection(call: SessionToolCall): boolean {
  return call.tool_kind === 'execute' || callPaths(call).length > 0
}

export function requestDescription(request: SessionRequest, call: SessionToolCall | undefined): React.ReactNode {
  if (request.kind !== 'permission' || !call) return request.title
  const text = call.content?.find((content) => content.type === 'text')?.text?.trim()
  if (text) {
    return call.tool_kind === 'switch_mode' ? <div className="whitespace-normal"><Clamped className="max-h-[12lh]"><Markdown text={text} /></Clamped></div> : text
  }
  return hasSection(call) ? undefined : call.title
}

/** The file name first, then its folder, so truncation drops the folder. */
function PathLabel({ path, suffix = '' }: { path: string; suffix?: string }) {
  const slash = path.lastIndexOf('/')
  return (
    <span className="font-code text-ui-sm">
      {path.slice(slash + 1)}{suffix}
      {slash > 0 && <span className="ml-1.5 text-muted">{path.slice(0, slash)}</span>}
    </span>
  )
}

function FileRows({ diffs }: { diffs: { path: string; patch: string }[] }) {
  const [open, setOpen] = useState<string>()
  const files = useMemo(() => diffs.map((diff) => patchFile(diff.path, diff.patch)), [diffs])
  return (
    <RequestRows
      rows={diffs.map((diff, index) => {
        const file = files[index]
        return (
          <RequestRow
            key={diff.path}
            open={open === diff.path}
            onOpenChange={(next) => setOpen(next ? diff.path : undefined)}
            meta={file && <Counts additions={file.additions} deletions={file.deletions} />}
            detail={file && <div className="max-h-64 overflow-auto rounded-control border border-seam"><DiffLines file={file} /></div>}
          >
            <PathLabel path={diff.path} />
          </RequestRow>
        )
      })}
    />
  )
}

function CallSection({ call }: { call: SessionToolCall }) {
  if (call.tool_kind === 'execute') {
    return (
      <RequestSection label="Command">
        <CodeBlock className="max-h-40 overflow-y-auto p-2 break-all whitespace-pre-wrap">{call.title}</CodeBlock>
      </RequestSection>
    )
  }
  if (call.diffs?.length) {
    return (
      <RequestSection label="Files" count={call.diffs.length}>
        <FileRows diffs={call.diffs} />
      </RequestSection>
    )
  }
  const locations = call.locations ?? []
  if (locations.length === 0) return null
  return (
    <RequestSection label={fileKinds.includes(call.tool_kind ?? '') ? 'Files' : 'Locations'} count={locations.length}>
      <RequestRows
        rows={locations.map((location, index) => (
          <RequestRow key={index} icon={<FileText />}>
            <PathLabel path={location.path} suffix={location.line ? `:${location.line}` : ''} />
          </RequestRow>
        ))}
      />
    </RequestSection>
  )
}

export interface WholeItem {
  loading: boolean
  cut: boolean
  error?: string
}

/** run.acp.item returns a wire-cut item whole, but one the log itself cut comes back still truncated. */
export function useWholeItem(sessionID: string, item: SessionItem | undefined): WholeItem {
  const [read, setRead] = useState<{ seq: number; error?: string }>()
  const truncated = Boolean(item?.truncated)
  const seq = item?.seq
  useEffect(() => {
    if (!truncated || seq === undefined) return
    loadWholeItem(useStore, api, sessionID, seq).then(() => setRead({ seq }), (err) => setRead({ seq, error: message(err) }))
  }, [truncated, sessionID, seq])
  const settled = truncated && read?.seq === seq
  return { loading: truncated && !settled, cut: settled && !read?.error, error: settled ? read?.error : undefined }
}

export function PermissionSections({ call, whole }: { call: SessionToolCall | undefined; whole: WholeItem }) {
  return (
    <>
      {whole.loading && <p className="border-t border-seam px-4 py-3 text-ui-sm text-muted">Loading the full change…</p>}
      {whole.error && <p role="alert" className="border-t border-seam px-4 py-3 text-ui-sm text-state-failed">The full change could not be read: {whole.error}</p>}
      {whole.cut && <p className="border-t border-seam px-4 py-3 text-ui-sm text-muted">This call is longer than the session log keeps, so the preview may be incomplete.</p>}
      {call && !whole.loading && <CallSection call={call} />}
    </>
  )
}

export function LinkSection({ url }: { url: string }) {
  return (
    <RequestSection>
      <div className="flex min-w-0 items-center gap-2">
        <Globe aria-hidden className="size-3.5 shrink-0 text-icon-faint" />
        <a href={url} target="_blank" rel="noreferrer" className="min-w-0 flex-1 truncate font-code text-ui-sm text-accent hover:underline">
          {url}
        </a>
        <Button variant="ghost" size="icon-sm" label="Copy link" onClick={(event) => void copyText(url, event.currentTarget)}><Copy /></Button>
        <Button variant="secondary" size="sm" onClick={() => window.open(url, '_blank', 'noreferrer')}><ExternalLink />Open</Button>
      </div>
    </RequestSection>
  )
}
