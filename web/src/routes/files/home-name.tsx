import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Input } from '@/components/ui/input'
import { errorSentence } from '@/lib/format'
import { indent } from '@/routes/files/tree'

function nameProblem(name: string): string | null {
  if (name === '') return 'Enter a name.'
  if (name.includes('/')) return 'A name cannot contain /.'
  if (name === '.' || name === '..') return `${name} is not a name.`
  return null
}

/** The row a new entry or a rename is typed into, where the entry sits in the
 * tree. Enter commits; Escape or leaving the field cancels. A refusal stays
 * under the field, which stays open. */
export function NameRow({
  depth,
  leading,
  label,
  initial = '',
  onCommit,
  onCancel,
}: {
  depth: number
  leading: ReactNode
  label: string
  initial?: string
  onCommit: (name: string) => Promise<void>
  onCancel: () => void
}) {
  const [name, setName] = useState(initial)
  const [error, setError] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const committing = useRef(false)
  const errorID = useId()

  useEffect(() => {
    const dot = initial.lastIndexOf('.')
    input.current?.focus()
    input.current?.setSelectionRange(0, dot > 0 ? dot : initial.length)
  }, [initial])

  return (
    <form
      className="flex min-w-0 items-start gap-2 pr-1"
      style={indent(depth)}
      onSubmit={(event) => {
        event.preventDefault()
        if (committing.current) return
        const problem = nameProblem(name)
        if (problem) return setError(problem)
        committing.current = true
        void onCommit(name).catch((err) => {
          committing.current = false
          setError(errorSentence(err))
        })
      }}
    >
      <span className="flex h-7 shrink-0 items-center gap-2 coarse:h-11 [&_svg]:size-3.5 [&_svg]:text-muted">{leading}</span>
      {/* Pulled left by the field's own inset, so the name sits where the row's label does. */}
      <span className="-ml-2 flex min-w-0 flex-1 flex-col">
        <Input
          ref={input}
          aria-label={label}
          aria-invalid={error !== null}
          aria-describedby={error === null ? undefined : errorID}
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          value={name}
          onChange={(event) => {
            setName(event.target.value)
            setError(null)
          }}
          onKeyDown={(event) => {
            if (event.key === 'Escape') onCancel()
          }}
          onBlur={() => {
            if (!committing.current) onCancel()
          }}
        />
        {error !== null && (
          <p id={errorID} role="alert" className="px-2 py-1 text-ui-sm text-state-failed">
            {error}
          </p>
        )}
      </span>
    </form>
  )
}
