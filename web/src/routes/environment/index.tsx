import { ViewHeader } from '@/components/view-header'
import { useIsMobile } from '@/lib/breakpoints'
import { TerminalDock } from '@/routes/environment/terminal-dock'
import { registerRoute } from '@/routes/registry'

function Environment() {
  const mobile = useIsMobile()
  return (
    <div className="flex h-full min-w-0 flex-col">
      {mobile && <ViewHeader title="Environment" />}
      <div className="flex min-h-0 flex-1 flex-col">
        <TerminalDock
          containment="fill"
          header={mobile ? undefined : (actions, hint) => <ViewHeader title="Environment" subtitle={hint} actions={actions || undefined} />}
        />
      </div>
    </div>
  )
}

registerRoute('environment', Environment)
