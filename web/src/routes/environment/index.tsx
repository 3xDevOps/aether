import { ViewHeader } from '@/components/view-header'
import { TerminalDock } from '@/routes/board/terminal-dock'
import { registerRoute } from '@/routes/registry'

function Environment() {
  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Environment" />
      <div className="min-h-0 flex-1">
        <TerminalDock containment="fill" />
      </div>
    </div>
  )
}

registerRoute('environment', Environment)
