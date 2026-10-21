import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { MemoryAddParams, MemoryAddResult, MemoryItem } from '@/lib/types'
import { kindLabel, writableKinds } from '@/routes/memory/model'

/** memory.add as a member, or with `replaces` the note that supersedes it.
 * A refusal is the server's text, unchanged; a refused pin still stores the
 * note, so every failure re-reads the list. */
export function AddNote({
  workspaceID,
  client,
  replaces,
  onAdded,
  onFailed,
  onClose,
}: {
  workspaceID: string
  client: Api
  replaces?: MemoryItem
  onAdded: (result: MemoryAddResult) => void
  onFailed: () => void
  onClose: () => void
}) {
  const [kind, setKind] = useState<MemoryAddParams['kind']>(replaces && replaces.kind !== 'outcome' ? replaces.kind : 'decision')
  const [text, setText] = useState(replaces?.body ?? '')
  const [files, setFiles] = useState(replaces?.files?.join('\n') ?? '')
  const [onlyMe, setOnlyMe] = useState(replaces?.scope === 'member')
  const [pin, setPin] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [action, pending] = replaces ? ['Replace note', 'Replacing…'] : ['Add note', 'Adding…']
  // The server moves a replaced note's pin, in its own scope, to the replacement.
  const pinMoves = Boolean(replaces?.pinned_at)

  const add = async () => {
    setBusy(true)
    setError('')
    const cited = files.split('\n').map((file) => file.trim()).filter(Boolean)
    try {
      onAdded(await client.memoryAdd({
        workspace_id: workspaceID,
        kind,
        text,
        ...(cited.length ? { files: cited } : {}),
        ...(replaces ? { supersedes: replaces.id } : {}),
        ...(onlyMe ? { scope: 'member' as const } : {}),
        ...(pin ? { pin } : {}),
      }))
    } catch (err) {
      setError(message(err))
      onFailed()
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{replaces ? `Replace ${replaces.id}` : 'Add a note'}</DialogTitle>
          <DialogDescription>
            {replaces
              ? 'The old note stays in history as Superseded. A pin on it moves to the replacement.'
              : 'Every run in this workspace can search it. Pin it to put its title in the brief new runs start with.'}
          </DialogDescription>
        </DialogHeader>
        <form
          id="memory-add"
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void add()
          }}
        >
          <div className="flex flex-col gap-1">
            <Label htmlFor="memory-add-kind">Kind</Label>
            <Select value={kind} onValueChange={(value) => setKind(value as MemoryAddParams['kind'])}>
              <SelectTrigger id="memory-add-kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {writableKinds.map((k) => <SelectItem key={k} value={k}>{kindLabel[k]}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>
          <FormField label="Note" help="The first line is the title, at most 120 bytes; the note holds at most 1024 bytes.">
            <Textarea autoFocus rows={5} value={text} onChange={(e) => setText(e.target.value)} />
          </FormField>
          <FormField label="Files" help="Repository paths the note cites, one per line: at most 20, each at most 512 bytes.">
            <Textarea rows={2} className="font-code text-ui-sm" value={files} onChange={(e) => setFiles(e.target.value)} />
          </FormField>
          <div className="flex items-start gap-2">
            <Checkbox id="memory-add-only-me" className="mt-0.5" checked={onlyMe} disabled={pinMoves} onCheckedChange={(checked) => setOnlyMe(checked === true)} />
            <div className="flex flex-col gap-0.5">
              <Label htmlFor="memory-add-only-me">Only me</Label>
              <p className="text-ui-sm text-muted">
                {pinMoves ? 'Follows the pin, which moves to the replacement.' : 'A pin on it enters only your own runs\' briefs. Everyone can still search it.'}
              </p>
            </div>
          </div>
          {!pinMoves && (
            <div className="flex items-start gap-2">
              <Checkbox id="memory-add-pin" className="mt-0.5" checked={pin} onCheckedChange={(checked) => setPin(checked === true)} />
              <div className="flex flex-col gap-0.5">
                <Label htmlFor="memory-add-pin">Pin</Label>
                <p className="text-ui-sm text-muted">{onlyMe ? 'Your new runs' : 'Every new run in this workspace'} gets its title in the brief.</p>
              </div>
            </div>
          )}
        </form>
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="memory-add" disabled={busy || !text.trim()}>{busy ? pending : action}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
