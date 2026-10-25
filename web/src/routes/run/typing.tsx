import { useEffect, useRef, useState } from 'react'
import { api } from '@/lib/api'
import { memberName } from '@/lib/member-name'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { runWatchers } from '@/store/presence'
import { activeTypers, nextTypingExpiry, typingLabel, typingThrottleMs } from '@/store/typing'

/** Tells the run's other watchers this member is typing: at most once per throttle window, and once more when the draft is sent or cleared. */
export function useTypingSignal(runID: string, body: string): void {
  const self = useSelf()
  const cap = useCapability()
  const shared = useStore((s) => runWatchers(s.presence, runID).some((id) => id !== self.id))
  const enabled = shared && cap.hasMethod('presence.typing')
  const sentAt = useRef<number | undefined>(undefined)
  const previous = useRef(body)
  const live = useRef(enabled)
  live.current = enabled

  useEffect(() => {
    const changed = previous.current !== body
    previous.current = body
    if (!changed) return
    if (!body.trim()) {
      if (sentAt.current !== undefined) void api.presenceTyping(runID, true).catch(() => {})
      sentAt.current = undefined
      return
    }
    const now = Date.now()
    if (!live.current || (sentAt.current !== undefined && now - sentAt.current < typingThrottleMs)) return
    sentAt.current = now
    void api.presenceTyping(runID).catch(() => {})
  }, [body, runID])

  useEffect(() => () => {
    if (sentAt.current !== undefined) void api.presenceTyping(runID, true).catch(() => {})
  }, [runID])
}

/** Who else is typing in this run, dropping each name when its entry expires. */
export function useTypers(runID: string): string[] {
  const self = useSelf()
  const entries = useStore((s) => s.typing[runID])
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const at = nextTypingExpiry(entries, Date.now())
    if (at === undefined) return
    const timer = window.setTimeout(() => setNow(Date.now()), at - Date.now() + 50)
    return () => window.clearTimeout(timer)
  }, [entries, now])
  return activeTypers(entries, Math.max(now, Date.now()), self.id ?? '')
}

/** The row keeps its height while others watch the run, so a name appearing never moves the composer. */
export function TypingIndicator({ runID }: { runID: string }) {
  const self = useSelf()
  const typers = useTypers(runID)
  const shared = useStore((s) => runWatchers(s.presence, runID).some((id) => id !== self.id))
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  if (!shared && typers.length === 0) return null
  const label = typingLabel(typers.map((id) => memberName({ members, mergedInto }, id)))
  return (
    <p role="status" aria-live="polite" className="h-4 truncate px-4 text-ui-sm leading-4 text-muted">
      {label && (
        <>
          {label}
          <span aria-hidden="true" className="ml-px">
            <span className="typing-dot">.</span>
            <span className="typing-dot">.</span>
            <span className="typing-dot">.</span>
          </span>
        </>
      )}
    </p>
  )
}
