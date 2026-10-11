import { useEffect, useId, useRef, useState } from 'react'
import type { RefObject } from 'react'
import { flushSync } from 'react-dom'
import { Input } from '@/components/ui/input'
import { portAddress } from '@/lib/ports'
import type { DevPort } from '@/lib/types'
import { cn, surface } from '@/lib/utils'

/** Shows the page's address until someone types; what they type then stays
 * until it is submitted, dropped with Escape, or the page moves on while the
 * field is not focused. While it has focus it offers the run's listening
 * ports that match what is typed. */
export function AddressBar({ url, going, disabled, ports, input, onSubmit, onOpened }: {
  /** The page's address. */
  url: string
  /** The address being opened, shown in its place until it is open or refused. */
  going: string
  disabled: boolean
  /** The listening ports to offer. */
  ports: DevPort[]
  input: RefObject<HTMLInputElement | null>
  /** Resolves true once the address was opened. */
  onSubmit: (address: string) => Promise<boolean>
  /** The address submitted here is open and nothing was typed since. */
  onOpened: () => void
}) {
  const [draft, setDraft] = useState<string | null>(null)
  const [offering, setOffering] = useState(false)
  const [chosen, setChosen] = useState(-1)
  const list = useId()
  const typing = useRef(draft)
  typing.current = draft
  const focused = useRef(false)
  const pressedIn = useRef(false)
  const shown = going || url
  useEffect(() => {
    if (!focused.current) setDraft(null)
  }, [url])

  const typed = (draft ?? '').trim().replace(/^https?:\/\//i, '')
  const offers = offering && !going ? ports.filter((port) => portAddress(port).includes(typed)) : []
  const active = chosen < offers.length ? chosen : -1
  const submit = (address: string) => {
    setOffering(false)
    void onSubmit(address).then((opened) => {
      // An address typed since then stays, and keeps the keyboard.
      if (!opened || (typing.current !== null && typing.current !== address)) return
      setDraft(null)
      onOpened()
    })
  }
  const pick = (port: DevPort) => {
    setDraft(portAddress(port))
    submit(portAddress(port))
  }

  return (
    <form
      className="min-w-24 flex-1 @md:relative"
      onSubmit={(event) => {
        event.preventDefault()
        submit(draft ?? shown)
      }}
    >
      <Input
        ref={input}
        role="combobox"
        aria-label="Address"
        aria-autocomplete="list"
        aria-expanded={offers.length > 0}
        aria-controls={offers.length > 0 ? list : undefined}
        aria-activedescendant={active >= 0 ? `${list}-${active}` : undefined}
        type="text"
        inputMode="url"
        enterKeyHint="go"
        autoCapitalize="off"
        autoCorrect="off"
        spellCheck={false}
        placeholder="localhost:3000"
        value={draft ?? shown}
        disabled={disabled}
        onChange={(event) => {
          setDraft(event.target.value)
          setOffering(true)
          setChosen(-1)
        }}
        onFocus={(event) => {
          focused.current = true
          setOffering(true)
          setChosen(-1)
          event.currentTarget.select()
        }}
        onBlur={() => {
          focused.current = false
          setOffering(false)
        }}
        // A press that focuses the field places a caret after onFocus has
        // selected the address, so the selection is made again on release.
        onMouseDown={(event) => { pressedIn.current = document.activeElement !== event.currentTarget }}
        onMouseUp={(event) => {
          const field = event.currentTarget
          if (pressedIn.current && field.selectionStart === field.selectionEnd) field.select()
          pressedIn.current = false
        }}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            if (!offering) setOffering(true)
            if (offers.length === 0) return
            event.preventDefault()
            const step = event.key === 'ArrowDown' ? 1 : -1
            setChosen(active < 0 && step < 0 ? offers.length - 1 : (active + step + offers.length) % offers.length)
          } else if (event.key === 'Enter' && active >= 0) {
            event.preventDefault()
            pick(offers[active]!)
          } else if (event.key === 'Escape') {
            const field = event.currentTarget
            flushSync(() => {
              setDraft(null)
              setOffering(false)
            })
            field.select()
          }
        }}
      />
      {offers.length > 0 && (
        <ul id={list} role="listbox" aria-label="Listening ports" className={cn(surface, 'absolute inset-x-2 top-full z-10 mt-1 max-h-56 overflow-y-auto p-1 @md:inset-x-0 @md:max-w-md')}>
          {offers.map((port, index) => (
            <li
              key={port.port}
              id={`${list}-${index}`}
              role="option"
              aria-selected={index === active}
              // The press must not take focus from the field it fills.
              onMouseDown={(event) => {
                event.preventDefault()
                pick(port)
              }}
              className={cn('flex h-7 min-w-0 cursor-pointer items-center gap-2 rounded-control px-2 text-ui-sm text-text hover:bg-hover-chrome coarse:h-11', index === active && 'bg-hover-chrome')}
            >
              <span className="shrink-0 font-code">{portAddress(port)}</span>
              {port.process && <span className="min-w-0 truncate text-muted">{port.process}</span>}
            </li>
          ))}
        </ul>
      )}
    </form>
  )
}
