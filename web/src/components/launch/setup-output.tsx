// Mirrors protocol.SetupFailure, the data of a launch the workspace setup
// script refused. Only the launching caller gets it.

import { CodeBlock } from '@/components/ui/code'
import { ApiError } from '@/lib/api'

export function setupOutput(err: unknown): string | null {
  const data = err instanceof ApiError ? (err.data as { setup_output?: unknown } | undefined) : undefined
  return typeof data?.setup_output === 'string' && data.setup_output !== '' ? data.setup_output : null
}

export function SetupOutput({ output }: { output: string }) {
  return (
    <div className="mt-2 flex min-w-0 flex-col gap-1">
      <p className="text-ui-sm text-muted">Setup script output</p>
      <CodeBlock aria-label="Setup script output" className="max-h-64 overflow-y-auto whitespace-pre-wrap">{output}</CodeBlock>
    </div>
  )
}
