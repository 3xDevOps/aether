// The one environment choice every workspace-creation form renders. Three
// cards - the published standard environment (recommended), the minimal
// starter, and a custom image - that emit the protocol environment object
// directly, so callers pass it to workspace.add without shaping anything.

import { useEffect, useId, useState } from 'react'
import { useStore } from '@/store'

/** Exactly one variant, mirroring protocol.WorkspaceEnvironment. */
export type EnvironmentValue =
  | { custom_image: string }
  | { neutral_image: true }

type Choice = 'standard' | 'starter' | 'custom'

const card =
  'flex cursor-pointer items-start gap-3 rounded-md border bg-card p-3 text-sm has-checked:border-ring'

const input =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50'

/**
 * Emits the chosen environment on mount and on every change, or null while
 * the choice is incomplete (custom card, nothing typed) so callers keep
 * submit disabled. An older server whose server.info lacks `standard_image`
 * gets no standard card, and the starter is preselected - today's default.
 * `onChange` must be referentially stable (a setState works).
 */
export function EnvironmentChoice({
  onChange,
}: {
  onChange: (environment: EnvironmentValue | null) => void
}) {
  const standardImage = useStore((s) => s.info?.standard_image)
  const [choice, setChoice] = useState<Choice>(
    standardImage ? 'standard' : 'starter',
  )
  const [custom, setCustom] = useState('')
  const group = useId()

  useEffect(() => {
    if (choice === 'standard' && standardImage) {
      onChange({ custom_image: standardImage })
    } else if (choice === 'custom') {
      onChange(custom.trim() ? { custom_image: custom.trim() } : null)
    } else {
      onChange({ neutral_image: true })
    }
  }, [choice, custom, standardImage, onChange])

  return (
    <fieldset className="space-y-2">
      <legend className="mb-1 text-sm">Environment</legend>
      {standardImage && (
        <label className={card}>
          <input
            type="radio"
            name={group}
            className="mt-0.5"
            checked={choice === 'standard'}
            onChange={() => setChoice('standard')}
            aria-label="Standard environment"
          />
          <span className="min-w-0 flex-1 space-y-0.5">
            <span className="flex items-center gap-2">
              <span className="font-medium">Standard environment</span>
              <span className="rounded-full border px-1.5 py-px text-[10px] uppercase tracking-wide text-muted-foreground">
                Recommended
              </span>
            </span>
            <span className="block text-xs text-muted-foreground">
              Go, Node, Python, and Rust ready to use, plus common build
              tools. Works for most projects with zero setup.
            </span>
            <span className="block truncate font-mono text-[10px] text-muted-foreground">
              {standardImage}
            </span>
          </span>
        </label>
      )}
      <label className={card}>
        <input
          type="radio"
          name={group}
          className="mt-0.5"
          checked={choice === 'starter'}
          onChange={() => setChoice('starter')}
          aria-label="Minimal starter"
        />
        <span className="min-w-0 flex-1 space-y-0.5">
          <span className="block font-medium">Minimal starter</span>
          <span className="block text-xs text-muted-foreground">
            A nearly empty container with just the basics; you install what
            the project needs yourself.
          </span>
        </span>
      </label>
      <label className={card}>
        <input
          type="radio"
          name={group}
          className="mt-0.5"
          checked={choice === 'custom'}
          onChange={() => setChoice('custom')}
          aria-label="Custom image"
        />
        <span className="min-w-0 flex-1 space-y-0.5">
          <span className="block font-medium">Custom image</span>
          <span className="block text-xs text-muted-foreground">
            An image you or your team already built and published.
          </span>
        </span>
      </label>
      {choice === 'custom' && (
        <label className="block space-y-1 text-sm">
          Image reference
          <input
            className={input}
            value={custom}
            placeholder="ghcr.io/you/dev-image:tag"
            onChange={(e) => setCustom(e.target.value)}
          />
        </label>
      )}
    </fieldset>
  )
}
