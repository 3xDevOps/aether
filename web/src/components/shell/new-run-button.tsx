import { type CSSProperties, type RefObject, useEffect, useId, useRef, useState } from 'react'
import { LoaderCircle, Moon, Plus, Sparkle, Terminal } from '@/components/icons'
import { type LastLaunch, quickLaunch, savedRefused, useLastLaunch } from '@/components/launch/quick-launch'
import { modeLabel } from '@/components/launch/modes'
import { splitShape } from '@/components/shell/split-shape'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button, type ButtonProps } from '@/components/ui/button'
import { SlideOut, useLingering } from '@/components/ui/slide-out'
import type { LaunchMode } from '@/lib/types'
import { cn } from '@/lib/utils'

const height = 28
const coarseHeight = 44
const lean = 8
const coarseLean = 10
// The halves overlap by the lean less the slash's gap, so the gap is 2px wherever the two meet.
const gap = 2

const fine = splitShape(height, lean)
const coarse = splitShape(coarseHeight, coarseLean)
const ringReach = 4
const ring = [4, 2].map((grow) => ({ fine: splitShape(height, lean, grow, ringReach), coarse: splitShape(coarseHeight, coarseLean, grow, ringReach) }))

const modeIcon: Record<LaunchMode, typeof Terminal> = { acp: Sparkle, tui: Terminal, headless: Moon }

function clip(first: boolean, shape: { fine: typeof fine; coarse: typeof coarse }): CSSProperties {
  const pick = first ? 'first' : 'second'
  return { '--clip': shape.fine[pick], '--clip-coarse': shape.coarse[pick] } as CSSProperties
}

const clipped = '[clip-path:var(--clip)] coarse:[clip-path:var(--clip-coarse)]'

function Half({ first, label, className, style, children, buttonRef, ...props }: {
  first: boolean
  label: string
  className?: string
  style?: CSSProperties
  children: React.ReactNode
  buttonRef?: RefObject<HTMLButtonElement | null>
} & Omit<ButtonProps, 'children' | 'style' | 'size'>) {
  return (
    <div className={cn('group/half pointer-events-none relative h-7 coarse:h-11', first ? 'min-w-0 flex-1' : 'w-11 shrink-0 coarse:w-14')} style={first ? undefined : { marginLeft: gap - lean }}>
      <span aria-hidden style={clip(first, { fine: ring[0]!.fine, coarse: ring[0]!.coarse })} className={cn('forced-color-adjust-none absolute -inset-1 bg-accent forced-colors:bg-[Highlight] invisible group-has-[button:focus-visible]/half:visible', clipped)} />
      <span aria-hidden style={clip(first, { fine: ring[1]!.fine, coarse: ring[1]!.coarse })} className={cn('absolute -inset-1 bg-chrome invisible group-has-[button:focus-visible]/half:visible', clipped)} />
      <Button
        ref={buttonRef}
        aria-label={label}
        style={{ ...clip(first, { fine, coarse }), ...style }}
        className={cn(
          'pointer-events-auto absolute inset-0 h-full rounded-none px-0 focus-visible:outline-0 pointer-fine:active:not-aria-disabled:scale-100 coarse:active:not-aria-disabled:brightness-100',
          clipped,
          className,
        )}
        {...props}
      >
        {children}
      </Button>
    </div>
  )
}

export function SplitNewRun({ blocked, shortcut, onOpen }: {
  blocked: string | null
  shortcut: string
  onOpen: () => void
}) {
  const { last, saved } = useLastLaunch()
  const [busy, setBusy] = useState(false)
  const [previewTop, setPreviewTop] = useState<number | null>(null)
  const shownTop = useRef(0)
  if (previewTop !== null) shownTop.current = previewTop
  const quick = useRef<HTMLButtonElement>(null)
  const previewID = useId()
  const lingering = useLingering(previewTop !== null)

  const showPreview = () => {
    const button = quick.current
    if (!button) return
    const sidebar = document.getElementById('sidebar')
    setPreviewTop(button.getBoundingClientRect().top - (sidebar?.getBoundingClientRect().top ?? 0))
  }

  useEffect(() => {
    if (previewTop === null) return
    const onKey = (event: KeyboardEvent) => event.key === 'Escape' && setPreviewTop(null)
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [previewTop])

  const needsTask = last?.mode === 'headless'
  const launchQuick = async () => {
    if (blocked || busy || !last || !saved.fresh) return
    if (needsTask || savedRefused(last, saved.result)) {
      onOpen()
      return
    }
    setPreviewTop(null)
    setBusy(true)
    await quickLaunch(last, saved.result)
    setBusy(false)
  }

  const ModeIcon = last ? modeIcon[last.mode] : null
  return (
    <div role="group" aria-label="New run" className="flex min-w-0 flex-1 items-center">
      <Half first label="New run" hint={blocked ?? `New run · ${shortcut}`} aria-disabled={blocked ? true : undefined} onClick={() => !blocked && onOpen()} className="pl-1 pr-3">
        <Plus />
        New run
      </Half>
      {last && ModeIcon && (
        <>
          <Half
            first={false}
            buttonRef={quick}
            label={`Launch again: ${last.harness}, ${modeLabel(last.mode)}`}
            aria-disabled={blocked || busy || !saved.fresh ? true : undefined}
            aria-describedby={lingering ? previewID : undefined}
            aria-busy={busy || undefined}
            onClick={() => void launchQuick()}
            onPointerEnter={showPreview}
            onPointerLeave={() => setPreviewTop(null)}
            onFocus={(event) => event.currentTarget.matches(':focus-visible') && showPreview()}
            onBlur={() => setPreviewTop(null)}
            className="pl-2"
          >
            {busy
              ? <LoaderCircle aria-hidden className="size-3.5 animate-spin motion-reduce:animate-none" />
              : (
                  <>
                    <AgentGlyph agent={last.harness} className="size-3.5 text-on-accent" />
                    <ModeIcon aria-hidden className="size-3 text-on-accent" />
                  </>
                )}
          </Half>
          {lingering && <LaunchPreview id={previewID} open={previewTop !== null} top={shownTop.current} last={last} needsTask={needsTask} />}
        </>
      )}
    </div>
  )
}

function LaunchPreview({ id, open, top, last, needsTask }: { id: string; open: boolean; top: number; last: LastLaunch; needsTask: boolean }) {
  return (
    <SlideOut open={open} top={top} width="16rem">
      <div id={id} role="tooltip" className="rounded-panel rounded-l-none border border-l-0 border-seam bg-raised p-3 text-ui shadow-overlay">
        <p className="text-ui-sm text-muted">{needsTask ? 'Opens New run with your last launch' : 'Launches again with no task'}</p>
        <dl className="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
          <dt className="text-muted">Agent</dt>
          <dd className="flex min-w-0 items-center gap-1.5"><AgentGlyph agent={last.harness} colored /><span className="truncate">{last.harness}</span></dd>
          <dt className="text-muted">Mode</dt>
          <dd>{modeLabel(last.mode)}</dd>
          <dt className="text-muted">Model</dt>
          <dd className="min-w-0 truncate">{last.model ?? 'Agent default'}</dd>
        </dl>
        {needsTask && <p className="mt-2 text-ui-sm text-muted">Background runs need a task.</p>}
      </div>
    </SlideOut>
  )
}
