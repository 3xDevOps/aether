import { type ReactNode, useId } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { SectionLabel } from '@/components/ui/section-label'
import { cn } from '@/lib/utils'

export function SettingsSection({ title, id: sectionID, heading = 'h3', scope, tone, children }: {
  title: string
  id?: string
  heading?: 'h2' | 'h3'
  scope?: string
  tone?: 'danger'
  children: ReactNode
}) {
  const id = useId()
  return (
    <section id={sectionID} aria-labelledby={id} className="group/section flex min-w-0 scroll-mt-4 flex-col gap-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <SectionLabel as={heading} id={id} tabIndex={-1} className="outline-none">{title}</SectionLabel>
        {scope && <Badge>{scope}</Badge>}
      </div>
      <div className={cn('flex min-w-0 flex-col divide-y divide-seam rounded-panel border transition-colors group-data-highlight/section:bg-selection motion-reduce:transition-none', tone === 'danger' ? 'border-state-failed/40' : 'border-seam')}>{children}</div>
    </section>
  )
}

export function SettingRow({
  id,
  label,
  help,
  control,
  children,
  labelFor,
}: {
  id?: string
  label: ReactNode
  help?: ReactNode
  control?: ReactNode
  children?: ReactNode
  labelFor?: string
}) {
  const Caption = labelFor ? 'label' : 'p'
  return (
    <div id={id} className="flex min-w-0 scroll-mt-4 flex-col gap-3 px-4 py-3 transition-colors data-highlight:bg-selection motion-reduce:transition-none">
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-6 gap-y-2">
        <div className="flex min-w-0 flex-[1_1_10rem] flex-col gap-0.5">
          <Caption htmlFor={labelFor} className="text-ui font-medium text-text">{label}</Caption>
          {help && <div className="text-ui-sm text-muted">{help}</div>}
        </div>
        {control && <div className="flex shrink-0 items-center gap-2 max-sm:has-[[data-slot=select-trigger]]:basis-full">{control}</div>}
      </div>
      {children}
    </div>
  )
}

export function TryAgain({ onClick }: { onClick: () => void }) {
  return <Button size="sm" variant="secondary" className="self-start" onClick={onClick}>Try again</Button>
}
