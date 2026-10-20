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
import type { MemoryAddParams, MemoryAddResult } from '@/lib/types'
import { kindLabel, writableKinds } from '@/routes/memory/model'

/** memory.add as a member. A refusal is the server's text, unchanged; a
 * refused pin still stores the note, so every failure re-reads the list. */
export function AddNote({
  workspaceID,
  client,
  onAdded,
  onFailed,
  onClose,
}: {
  workspaceID: string
  client: Api
  onAdded: (result: MemoryAddResult) => void
  onFailed: () => void
  onClose: () => void
}) {
  const [kind, setKind] = useState<MemoryAddParams['kind']>('decision')
  const [text, setText] = useState('')
  const [onlyMe, setOnlyMe] = useState(false)
  const [pin, setPin] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const add = async () => {
    setBusy(true)
    setError('')
    try {
      onAdded(await client.memoryAdd({ workspace_id: workspaceID, kind, text, ...(onlyMe ? { scope: 'member' as const } : {}), ...(pin ? { pin } : {}) }))
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
          <DialogTitle>Add a note</DialogTitle>
          <DialogDescription>Every run in this workspace can search it. Pin it to put its title in the brief new runs start with.</DialogDescription>
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
          <div className="flex items-start gap-2">
            <Checkbox id="memory-add-only-me" className="mt-0.5" checked={onlyMe} onCheckedChange={(checked) => setOnlyMe(checked === true)} />
            <div className="flex flex-col gap-0.5">
              <Label htmlFor="memory-add-only-me">Only me</Label>
              <p className="text-ui-sm text-muted">A pin on it enters only your own runs&apos; briefs. Everyone can still search it.</p>
            </div>
          </div>
          <div className="flex items-start gap-2">
            <Checkbox id="memory-add-pin" className="mt-0.5" checked={pin} onCheckedChange={(checked) => setPin(checked === true)} />
            <div className="flex flex-col gap-0.5">
              <Label htmlFor="memory-add-pin">Pin</Label>
              <p className="text-ui-sm text-muted">{onlyMe ? 'Your new runs' : 'Every new run in this workspace'} gets its title in the brief.</p>
            </div>
          </div>
        </form>
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="memory-add" disabled={busy || !text.trim()}>{busy ? 'Adding…' : 'Add note'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
