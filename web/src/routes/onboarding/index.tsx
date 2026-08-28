// The onboarding wizard: the quickstart's most error-prone stretch - link,
// workspace, environment, repo remote, first run - as five steps. It exists
// only where the gateway has this machine's SSH identity and filesystem, so
// the whole route gates on the link.status local verb; a remote gateway gets
// an empty state, not a broken wizard. Nothing persists: every mount
// re-checks link status.

import { useState } from 'react'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import type { Workspace } from '@/lib/types'
import {
  EnvironmentReview,
  EnvironmentStep,
  type EnvScanReview,
} from '@/routes/onboarding/environment-step'
import {
  FirstRunStep,
  LinkStep,
  RepoStep,
  WorkspaceStep,
} from '@/routes/onboarding/steps'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useCapability } from '@/store/hooks'

const steps = ['Link', 'Workspace', 'Environment', 'Repository', 'First run'] as const

export function OnboardingRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  const [step, setStep] = useState(0)
  const [workspace, setWorkspace] = useState<Workspace | null>(null)
  const [review, setReview] = useState<EnvScanReview | null>(null)

  if (!caps.hasLocal('link.status')) {
    return (
      <div className="flex h-full flex-col">
        <ViewHeader title="Onboarding" />
        <div className="flex flex-1 items-center justify-center p-4">
          <p className="max-w-md text-center text-sm text-muted-foreground">
            Onboarding runs in the desktop app or `aether gui`, where the
            gateway holds your SSH identity and can reach your local
            repositories. This gateway is a remote monitor.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title="Onboarding" subtitle={steps[step]} />
      <div className="flex-1 space-y-4 overflow-y-auto p-4">
        <ol aria-label="Steps" className="flex gap-2 text-xs">
          {steps.map((label, i) => (
            <li
              key={label}
              aria-current={i === step ? 'step' : undefined}
              className={
                i === step
                  ? 'rounded-sm border px-1.5 py-0.5 font-medium'
                  : 'rounded-sm border px-1.5 py-0.5 text-muted-foreground'
              }
            >
              {i + 1}. {label}
            </li>
          ))}
        </ol>

        {step === 0 && <LinkStep client={client} onNext={() => setStep(1)} />}
        {step === 1 && (
          <WorkspaceStep
            client={client}
            caps={caps}
            onNext={(w) => {
              setWorkspace(w)
              setStep(2)
            }}
          />
        )}
        {step === 2 && review === null && (
          <EnvironmentStep
            client={client}
            onNext={() => setStep(3)}
            onReview={setReview}
          />
        )}
        {step === 2 && review !== null && (
          <EnvironmentReview
            client={client}
            workspaceId={workspace?.id}
            review={review}
            // Advancing clears the review, so Back from Repository lands
            // on the choice cards rather than a stale review gate.
            onDone={() => {
              setReview(null)
              setStep(3)
            }}
            onKeep={() => {
              setReview(null)
              setStep(3)
            }}
          />
        )}
        {step === 3 && (
          <RepoStep client={client} workspace={workspace} onNext={() => setStep(4)} />
        )}
        {step === 4 && <FirstRunStep client={client} workspace={workspace} />}

        {step > 0 && (
          <button
            type="button"
            className="text-xs text-muted-foreground underline-offset-2 hover:underline"
            onClick={() => {
              // Backing out of the review returns to the choice cards
              // rather than leaving the step.
              if (step === 2 && review !== null) {
                setReview(null)
                return
              }
              setStep(step - 1)
            }}
          >
            Back
          </button>
        )}
      </div>
    </div>
  )
}

registerRoute('onboarding', OnboardingRoute)
