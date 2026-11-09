// Mirrors protocol.SetupFailure, the data of a launch the workspace setup
// script refused, and protocol.MirrorFailure's adopt_command. Only the
// launching caller gets them.

import { Fragment, useRef } from 'react'
import { CodeBlock } from '@/components/ui/code'
import { CopyButton } from '@/components/ui/copy-button'
import { ApiError } from '@/lib/api'

export function setupOutput(err: unknown): string | null {
  const data = err instanceof ApiError ? (err.data as { setup_output?: unknown } | undefined) : undefined
  return typeof data?.setup_output === 'string' && data.setup_output !== '' ? data.setup_output : null
}

export function adoptCommand(err: unknown): string | null {
  const data = err instanceof ApiError ? (err.data as { adopt_command?: unknown } | undefined) : undefined
  return typeof data?.adopt_command === 'string' && data.adopt_command !== '' ? data.adopt_command : null
}

export function AdoptCommand({ command }: { command: string }) {
  const code = useRef<HTMLPreElement>(null)
  return (
    <div className="mt-2 flex min-w-0 items-start gap-1">
      <CodeBlock ref={code} aria-label="Adopt command" className="min-w-0 flex-1 whitespace-pre-wrap [overflow-wrap:anywhere]">
        {command.split(' ').map((token, i) => <Fragment key={i}>{i > 0 && ' '}{token.startsWith('-') ? <span className="whitespace-nowrap">{token}</span> : token}</Fragment>)}
      </CodeBlock>
      <CopyButton text={command} label="Copy the adopt command" source={code} />
    </div>
  )
}

export function SetupOutput({ output }: { output: string }) {
  return (
    <div className="mt-2 flex min-w-0 flex-col gap-1">
      <p className="text-ui-sm text-muted">Setup script output</p>
      <CodeBlock aria-label="Setup script output" className="max-h-64 overflow-y-auto whitespace-pre-wrap">{output}</CodeBlock>
    </div>
  )
}
