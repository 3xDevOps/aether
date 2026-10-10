import { useMemo, useRef, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Code } from '@/components/ui/code'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { parseDotenv } from '@/lib/dotenv'
import { message } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'

function skipped(lines: number[]): string {
  if (lines.length === 1) return `Line ${lines[0]} is not NAME=VALUE and is skipped.`
  return `Lines ${lines.slice(0, -1).join(', ')} and ${lines[lines.length - 1]} are not NAME=VALUE and are skipped.`
}

export function WorkspaceEnvironmentImport({ current, onAdd, onClose }: {
  /** Names already in the list, which an import replaces. */
  current: string[]
  onAdd: (variables: { name: string; value: string }[], secret: boolean) => void
  onClose: () => void
}) {
  const [text, setText] = useState('')
  const [secret, setSecret] = useState(true)
  const [fileError, setFileError] = useState<string | null>(null)
  const picker = useRef<HTMLInputElement>(null)
  const returnFocus = useReturnFocus()
  const { variables, badLines } = useMemo(() => parseDotenv(text), [text])
  const count = variables.length

  const read = async (file: File | undefined) => {
    if (!file) return
    setFileError(null)
    try {
      setText(await file.text())
    } catch (err) {
      setFileError(`${file.name}: ${message(err)}`)
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-[min(560px,calc(100%-2rem))]" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Import .env</DialogTitle>
          <DialogDescription>
            Paste <Code>NAME=VALUE</Code> lines or choose a file. They are added to the list; nothing is saved until you press Save.
          </DialogDescription>
        </DialogHeader>
        <form
          id="workspace-environment-import"
          className="flex min-w-0 flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault()
            if (count === 0) return
            onAdd(variables, secret)
            onClose()
          }}
        >
          <Textarea
            aria-label="NAME=VALUE lines"
            rows={6}
            autoFocus
            className="font-code text-ui-sm"
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            placeholder={'API_URL=https://api.example.test\nFEATURE_FLAG=on'}
            value={text}
            onChange={(event) => setText(event.target.value)}
          />
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <label className="flex items-center gap-1.5 text-ui text-text">
              <Checkbox checked={secret} onCheckedChange={(next) => setSecret(next === true)} />
              Store as secrets
            </label>
            <input
              ref={picker}
              type="file"
              className="sr-only"
              tabIndex={-1}
              aria-label="Choose a .env file"
              onChange={(event) => {
                const file = event.currentTarget.files?.[0]
                event.currentTarget.value = ''
                void read(file)
              }}
            />
            <Button type="button" size="sm" variant="secondary" onClick={() => picker.current?.click()}>Choose file…</Button>
          </div>
          {fileError && <Callout tone="failed" role="alert">{fileError}</Callout>}
          {badLines.length > 0 && <Callout tone="needs-you" role="status">{skipped(badLines)}</Callout>}
          {count > 0 && (
            <ul aria-label="Variables to add" className="flex max-h-48 min-w-0 flex-col gap-1.5 overflow-y-auto">
              {variables.map(({ name }) => (
                <li key={name} className="flex min-w-0 items-center justify-between gap-3">
                  <Code>{name}</Code>
                  <span className="flex shrink-0 items-center gap-2 text-ui-sm text-muted">
                    {current.includes(name) ? 'Replaces the current value' : 'New'}
                    {secret && <Badge>Secret</Badge>}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </form>
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="workspace-environment-import" disabled={count === 0}>
            {count === 0 ? 'Add variables' : `Add ${count} ${count === 1 ? 'variable' : 'variables'}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
