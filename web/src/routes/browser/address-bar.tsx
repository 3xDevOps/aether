import { useEffect, useRef, useState } from 'react'
import type { RefObject } from 'react'
import { flushSync } from 'react-dom'
import { Input } from '@/components/ui/input'

/** Shows the page's address until someone types; what they type then stays
 * until it is submitted, dropped with Escape, or the page moves on while the
 * field is not focused. */
export function AddressBar({ url, disabled, input, onSubmit }: {
  url: string
  disabled: boolean
  input: RefObject<HTMLInputElement | null>
  /** Resolves true once the address was opened. */
  onSubmit: (address: string) => Promise<boolean>
}) {
  const [draft, setDraft] = useState<string | null>(null)
  const focused = useRef(false)
  const pressedIn = useRef(false)
  useEffect(() => {
    if (!focused.current) setDraft(null)
  }, [url])
  return (
    <form
      className="min-w-24 flex-1"
      onSubmit={(event) => {
        event.preventDefault()
        void onSubmit(draft ?? url).then((opened) => { if (opened) setDraft(null) })
      }}
    >
      <Input
        ref={input}
        aria-label="Address"
        type="text"
        inputMode="url"
        enterKeyHint="go"
        autoCapitalize="off"
        autoCorrect="off"
        spellCheck={false}
        placeholder="localhost:3000"
        value={draft ?? url}
        disabled={disabled}
        onChange={(event) => setDraft(event.target.value)}
        onFocus={(event) => {
          focused.current = true
          event.currentTarget.select()
        }}
        onBlur={() => { focused.current = false }}
        // A press that focuses the field places a caret after onFocus has
        // selected the address, so the selection is made again on release.
        onMouseDown={(event) => { pressedIn.current = document.activeElement !== event.currentTarget }}
        onMouseUp={(event) => {
          const field = event.currentTarget
          if (pressedIn.current && field.selectionStart === field.selectionEnd) field.select()
          pressedIn.current = false
        }}
        onKeyDown={(event) => {
          if (event.key !== 'Escape') return
          const field = event.currentTarget
          flushSync(() => setDraft(null))
          field.select()
        }}
      />
    </form>
  )
}
