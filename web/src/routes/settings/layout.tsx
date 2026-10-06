import { type ReactNode, useId } from 'react'
import { SectionLabel } from '@/components/ui/section-label'

export function SettingsSection({ title, children }: { title: string; children: ReactNode }) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h2" id={id}>{title}</SectionLabel>
      <div className="flex min-w-0 flex-col divide-y divide-seam rounded-panel border border-seam">{children}</div>
    </section>
  )
}

export function SettingRow({
  label,
  help,
  control,
  children,
  labelFor,
}: {
  label: ReactNode
  help?: ReactNode
  control?: ReactNode
  children?: ReactNode
  labelFor?: string
}) {
  const Caption = labelFor ? 'label' : 'p'
  return (
    <div className="flex min-w-0 flex-col gap-3 px-4 py-3">
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-6 gap-y-2">
        <div className="flex min-w-0 flex-[1_1_10rem] flex-col gap-0.5">
          <Caption htmlFor={labelFor} className="text-ui font-medium text-text">{label}</Caption>
          {help && <div className="text-ui-sm text-muted">{help}</div>}
        </div>
        {control && <div className="flex shrink-0 items-center gap-2">{control}</div>}
      </div>
      {children}
    </div>
  )
}
