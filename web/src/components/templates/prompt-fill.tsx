import { useId, useLayoutEffect, useRef, useState, type RefObject } from 'react'
import { ArrowRight, Check, Copy } from '@/components/icons'
import { effective, missingNames, own, placeholderNames, renderTask, segments, tokenEstimate } from '@/components/templates/placeholders'
import { Button } from '@/components/ui/button'
import { IconSwap, TextSwap } from '@/components/ui/text-swap'
import { copyText } from '@/lib/clipboard'
import { useTimedFlag } from '@/lib/hooks'
import { playEntrance } from '@/lib/motion'
import type { Template } from '@/lib/types'
import { cn } from '@/lib/utils'

export type Typed = Record<string, string>

/** Values typed per template, kept while the library stays mounted so switching templates loses nothing. */
export function useTypedValues() {
  const [typed, setTyped] = useState<Record<string, Typed>>({})
  const key = (template: Template) => JSON.stringify([template.workspace_id, template.name])
  return {
    of: (template: Template): Typed => typed[key(template)] ?? {},
    set: (template: Template, name: string, value: string) =>
      setTyped((all) => ({ ...all, [key(template)]: { ...all[key(template)], [name]: value } })),
  }
}

/** Plays the detail entrance when `key` changes, never on mount. */
export function useEntranceOnChange(ref: RefObject<HTMLElement | null>, key: string) {
  const shown = useRef(key)
  useLayoutEffect(() => {
    if (shown.current === key) return
    shown.current = key
    if (!ref.current) return
    const animations = playEntrance(ref.current, { motion: { duration: '--duration-220', easing: '--ease-standard' } })
    return () => animations.forEach((a) => a.cancel())
  }, [key, ref])
}

const fieldBox = 'relative inline-grid h-5.5 max-w-full rounded-chip px-1.5 align-middle leading-5.5 whitespace-pre'
const filledLook = 'bg-accent-soft font-medium text-accent'
const emptyLook = 'bg-hover-chrome text-text ring-1 ring-seam ring-inset'

function hintFor(template: Template, name: string): string {
  const fallback = own(template.params, name)
  return fallback === undefined ? 'required, no default' : `default: ${fallback || 'empty'}`
}

export function FillProgress({ template, typed }: { template: Template; typed: Typed }) {
  const names = placeholderNames(template.task)
  if (names.length === 0) return null
  const filled = names.length - missingNames(template, typed).length
  return (
    <span className="flex shrink-0 items-center gap-2 text-ui-sm text-muted tabular-nums">
      <span aria-hidden className="flex items-center gap-0.5 max-sm:hidden">
        {names.map((name, i) => (
          <span
            key={name}
            className={cn('size-1.5 rounded-full transition-colors duration-250 motion-reduce:transition-none', i < filled ? 'bg-accent' : 'bg-seam')}
          />
        ))}
      </span>
      {filled} of {names.length} filled
    </span>
  )
}

/**
 * The task as running text with an inline field at each placeholder's first occurrence; later occurrences mirror it.
 * Enter moves to the next unfilled field, or calls `onComplete` once none is left.
 */
export function PromptFill({
  template,
  typed,
  onValue,
  onHint,
  onComplete,
}: {
  template: Template
  typed: Typed
  onValue: (name: string, value: string) => void
  onHint: (name: string | null) => void
  onComplete: () => void
}) {
  const fields = useRef(new Map<string, HTMLInputElement>())
  const captionID = useId()
  const parts = segments(template.task)
  const names = placeholderNames(template.task)
  const next = (from: string) => {
    const missing = missingNames(template, typed)
    if (missing.length === 0) return onComplete()
    const after = names.slice(names.indexOf(from) + 1).find((name) => missing.includes(name))
    fields.current.get(after ?? missing[0]!)?.focus()
  }
  return (
    <>
      <p className="text-ui leading-7 break-words whitespace-pre-wrap text-text">
        {parts.map((part, i) => {
          if ('text' in part) return <span key={i}>{part.text}</span>
          const value = effective(template, typed, part.name)
          if (!part.first) {
            return (
              <span key={i} className={cn(fieldBox, value === undefined ? cn(emptyLook, 'text-muted') : filledLook)}>
                <span className="min-w-0 truncate">{value || part.name}</span>
              </span>
            )
          }
          const fallback = own(template.params, part.name)
          const shown = own(typed, part.name) ?? fallback ?? ''
          return (
            <span
              key={i}
              className={cn(
                fieldBox,
                'transition-colors duration-130 focus-within:outline-1 focus-within:-outline-offset-1 focus-within:outline-accent focus-within:transition-none motion-reduce:transition-none',
                value === undefined ? emptyLook : filledLook,
              )}
            >
              <span aria-hidden className="invisible col-start-1 row-start-1 grid min-w-0 overflow-hidden">
                <span className="col-start-1 row-start-1 font-normal">{fallback || part.name}</span>
                <span className="col-start-1 row-start-1">{shown}</span>
              </span>
              <input
                ref={(el) => {
                  if (el) fields.current.set(part.name, el)
                  else fields.current.delete(part.name)
                }}
                type="text"
                value={shown}
                placeholder={fallback || part.name}
                aria-label={`${part.name} (${hintFor(template, part.name)})`}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => onValue(part.name, e.target.value)}
                onFocus={() => onHint(part.name)}
                onBlur={() => onHint(null)}
                onKeyDown={(e) => {
                  if (e.key !== 'Enter' || e.nativeEvent.isComposing) return
                  e.preventDefault()
                  next(part.name)
                }}
                className="col-start-1 row-start-1 h-full w-0 min-w-full bg-transparent p-0 text-ellipsis text-inherit outline-none placeholder:font-normal placeholder:text-muted"
              />
            </span>
          )
        })}
      </p>
      {names.length > 0 && (
        <div className="flex flex-col gap-1">
          <p id={captionID} className="text-ui-sm text-muted">Preview</p>
          <div
            role="region"
            aria-labelledby={captionID}
            className="max-h-64 overflow-y-auto rounded-control border border-seam bg-chrome px-3 py-2 text-ui break-words whitespace-pre-wrap text-muted"
          >
            {parts.map((part, i) => {
              if ('text' in part) return <span key={i}>{part.text}</span>
              const value = effective(template, typed, part.name)
              return value === undefined
                ? <span key={i} className="font-code">{`{{${part.name}}}`}</span>
                : <span key={i} className="font-medium text-text">{value}</span>
            })}
          </div>
        </div>
      )}
    </>
  )
}

export type PrimaryAction = {
  /** The visible verb, such as Launch or Insert. */
  text: string
  /** The accessible name when the verb alone is ambiguous. */
  label?: string
  /** Shown in place of `text`, with the button disabled, while the action is in flight. */
  pending?: string
  disabled?: boolean
  run: () => void
}

export function runIfReady(action: PrimaryAction) {
  if (!action.disabled && !action.pending) action.run()
}

/**
 * The footer band: a status slot, Copy and the primary action, which stays aria-disabled until every placeholder has a
 * value. `className` styles the band's row, such as a content column.
 */
export function PromptFooter({ template, typed, hint, primary, className }: {
  template: Template
  typed: Typed
  hint: string | null
  primary: PrimaryAction
  className?: string
}) {
  const [copied, flashCopied] = useTimedFlag(2000)
  const [asking, setAsking] = useState(false)
  const missing = missingNames(template, typed).length
  const text = renderTask(template, typed)
  const shownHint = hint && placeholderNames(template.task).includes(hint) ? hint : null
  const state = copied ? 'copied' : asking && missing > 0 ? 'fill' : shownHint ? `hint:${shownHint}` : 'tokens'
  const ask = (on: boolean) => () => setAsking(on)
  return (
    <div data-slot="prompt-footer" className="@container shrink-0 border-t border-seam bg-chrome">
      <div className={cn('flex h-11 items-center gap-2 px-4 coarse:h-14', className)}>
        <span className="min-w-0 flex-1 text-ui-sm text-muted">
          <TextSwap swapKey={state} mode="crossfade" className="w-full" layerClassName="min-w-0 truncate">
            {state === 'copied' ? (
              <span className="inline-flex items-center gap-1 text-accent"><Check aria-hidden className="size-3" />Copied to clipboard</span>
            ) : state === 'fill' ? (
              `Fill ${missing} more to ${primary.text.toLowerCase()}`
            ) : shownHint ? (
              <><span className="font-medium text-text">{shownHint}</span> · {hintFor(template, shownHint)}</>
            ) : (
              `~${tokenEstimate(text)} tokens`
            )}
          </TextSwap>
        </span>
        <span role="status" className="sr-only">{copied ? 'Prompt copied' : ''}</span>
        <Button
          variant="secondary"
          size="sm"
          aria-label="Copy prompt"
          onClick={async () => {
            if (await copyText(text, null, { quiet: true })) flashCopied()
          }}
        >
          <IconSwap swapped={copied} from={<Copy aria-hidden className="size-3" />} to={<Check aria-hidden className="size-3 text-accent" />} />
          <span className="hidden @min-[28rem]:inline">Copy</span>
        </Button>
        <Button
          size="sm"
          aria-label={primary.pending ? undefined : primary.label}
          aria-disabled={missing > 0 || undefined}
          disabled={primary.disabled || Boolean(primary.pending)}
          onClick={() => missing === 0 && primary.run()}
          onPointerEnter={ask(true)}
          onPointerLeave={ask(false)}
          onFocus={ask(true)}
          onBlur={ask(false)}
        >
          {primary.pending ?? primary.text}
          <ArrowRight aria-hidden className="size-3" />
        </Button>
      </div>
    </div>
  )
}
