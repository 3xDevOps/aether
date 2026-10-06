import { useRef, useState } from 'react'
import type * as React from 'react'
import { Paperclip, Send, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

const maxAttachments = 8
const imageTypes = 'image/png,image/jpeg,image/gif,image/webp'

export function composerBlock(run: RunRecord, maySteer: boolean, canReopen: boolean): string | null {
  if (isTerminal(run.status)) return canReopen ? 'This run has finished. Reopen it from More to message the agent.' : 'This run has finished.'
  if (run.status === 'queued' || run.status === 'provisioning') return 'The agent is still starting.'
  if (!maySteer) return run.protected ? 'This run is protected: only its owner or an admin can message the agent.' : 'You can watch this run but not message the agent.'
  return null
}

export function Composer({ run, agent, room, textarea, onFocusChange }: {
  run: RunRecord
  agent: AgentTerminal
  room: RunRoom
  textarea: React.RefObject<HTMLTextAreaElement | null>
  onFocusChange: (focused: boolean) => void
}) {
  const self = useSelf()
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const coarse = useMediaQuery(coarsePointer)
  const [body, setBody] = useState('')
  const [attachments, setAttachments] = useState<string[]>([])
  const [uploading, setUploading] = useState(false)
  const picker = useRef<HTMLInputElement>(null)
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const block = composerBlock(run, maySteer, cap.hasMethod('run.relaunch') && run.mode === 'tui')
  const hint = agent.localControl
    ? 'You control this run, so your message reaches the agent now.'
    : 'Your message waits 45 s before it reaches the agent. Whoever controls the run can deliver or deny it sooner.'

  const send = async () => {
    const text = body.trim()
    if (!text || room.busy || uploading) return
    if (await room.post({ kind: 'steer_request', body: text, attachments })) {
      setBody('')
      setAttachments([])
    }
  }

  const upload = async (file: File) => {
    if (uploading || attachments.length >= maxAttachments) return
    setUploading(true)
    try {
      const result = await api.uploadTerminalImage(file, run.id)
      setAttachments((current) => [...current, result.path].slice(0, maxAttachments))
    } catch (cause) {
      useStore.getState().setRoomActionError(run.id, message(cause))
    } finally {
      setUploading(false)
    }
  }

  if (block) {
    return (
      <div className="shrink-0 border-t border-seam bg-canvas">
        <p role="status" className="mx-auto max-w-[736px] px-4 py-3 text-ui-sm text-muted">{block}</p>
      </div>
    )
  }

  return (
    <div className="shrink-0 border-t border-seam bg-canvas pb-[var(--keyboard-inset,0px)]">
      <div className="mx-auto flex max-w-[736px] flex-col gap-2 px-4 py-3">
        <Textarea
          ref={textarea}
          aria-label="Message the agent"
          aria-describedby="composer-hint"
          rows={2}
          value={body}
          placeholder="Message the agent"
          className="resize-none [field-sizing:content] max-md:[--composer-lines:6.5rem]"
          style={{ maxHeight: 'var(--composer-lines, 10rem)' }}
          onFocus={() => onFocusChange(true)}
          onBlur={() => onFocusChange(false)}
          onChange={(event) => setBody(event.target.value)}
          onKeyDown={(event) => {
            if (event.nativeEvent.isComposing || event.key !== 'Enter' || !(event.metaKey || event.ctrlKey)) return
            event.preventDefault()
            void send()
          }}
        />
        <div className="flex min-w-0 items-center gap-2">
          <input
            ref={picker}
            type="file"
            accept={imageTypes}
            className="sr-only"
            tabIndex={-1}
            onChange={(event) => {
              const file = event.target.files?.[0]
              if (file) void upload(file)
              event.currentTarget.value = ''
            }}
          />
          <Button
            variant="ghost"
            size="icon-sm"
            label={attachments.length >= maxAttachments ? `At most ${maxAttachments} images` : 'Attach an image'}
            disabled={uploading || attachments.length >= maxAttachments}
            onClick={() => picker.current?.click()}
          >
            <Paperclip />
          </Button>
          {attachments.length > 0 && (
            <Button variant="ghost" size="sm" hint="Remove the attached images" onClick={() => setAttachments([])}>
              {attachments.length} {attachments.length === 1 ? 'image' : 'images'}
              <X />
            </Button>
          )}
          <p id="composer-hint" className="min-w-0 flex-1 truncate text-ui-sm text-muted" title={hint}>
            {uploading ? 'Uploading…' : hint}
          </p>
          <Button
            size="sm"
            hint={coarse ? undefined : 'Send (Ctrl+Enter)'}
            disabled={!body.trim() || room.busy || uploading}
            onClick={() => void send()}
          >
            <Send />
            {room.busy ? 'Sending…' : 'Send'}
          </Button>
        </div>
      </div>
    </div>
  )
}
