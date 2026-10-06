import type * as React from 'react'
import { Checkbox } from '@/components/ui/checkbox'
import { CodeBlock } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import type { RunGitExpected, RunRepoCommandOutput } from '@/lib/types'

export function InlineError({ children }: { children?: string }) {
  if (!children) return null
  return (
    <p role="alert" className="text-ui-sm break-words whitespace-pre-wrap text-state-failed">
      {children}
    </p>
  )
}

export function Actual({ actual }: { actual?: RunGitExpected }) {
  if (!actual) return null
  return (
    <p role="alert" className="text-ui-sm break-all text-state-failed">
      Actual branch / HEAD: <code className="font-code">{actual.branch} / {actual.head}</code>. Refresh and review before another action.
    </p>
  )
}

export function Diagnostics({ output, error }: { output: RunRepoCommandOutput; error?: string }) {
  const shown = output.stdout || output.stderr || output.exit_code !== 0 || output.truncated
  return (
    <>
      <InlineError>{error}</InlineError>
      {shown && (
        <Collapsible defaultOpen={!!error || output.exit_code !== 0} className="min-w-0">
          <CollapsibleTrigger>
            <span className="text-ui-sm text-muted">
              Native diagnostics · exit {output.exit_code}
              {output.truncated ? ' · truncated' : ''}
            </span>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-1 max-h-64 overflow-y-auto">
              <CodeBlock className="whitespace-pre-wrap break-all">
                {output.stdout}
                {output.stderr}
              </CodeBlock>
            </div>
          </CollapsibleContent>
        </Collapsible>
      )}
    </>
  )
}

export function Outcome({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <section aria-label={label} className="grid gap-1 rounded-panel border border-seam bg-chrome p-2 text-ui-sm">
      {children}
    </section>
  )
}

export function Attest({
  checked,
  disabled,
  onChange,
  children,
}: {
  checked: boolean
  disabled?: boolean
  onChange: (checked: boolean) => void
  children: React.ReactNode
}) {
  return (
    <label className="flex items-start gap-2 text-ui text-text has-[:disabled]:text-muted">
      <Checkbox className="mt-0.5" checked={checked} disabled={disabled} onCheckedChange={(next) => onChange(next === true)} />
      <span>{children}</span>
    </label>
  )
}

export function Heading({ id, children }: { id?: string; children: React.ReactNode }) {
  return (
    <h3 id={id} className="text-ui font-semibold text-text">
      {children}
    </h3>
  )
}
