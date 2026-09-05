// The onboarding wizard: the quickstart's most error-prone stretch - link,
// workspace, repo remote, agents, first run - as five steps. It exists only
// where the gateway has this machine's SSH identity and filesystem, so the
// whole route gates on the link.status local verb; a remote gateway gets an
// empty state, not a broken wizard. Step and workspace choices persist so a
// reload resumes where the user left off.

import { useState } from 'react'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import {
  FirstRunStep,
  LinkStep,
  RepoStep,
  WorkspaceStep,
} from '@/routes/onboarding/steps'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const steps = [
  'Link',
  'Workspace',
  'Repository',
  'Agents',
  'First run',
] as const

export function OnboardingRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  const persistedStep = useStore((s) => s.onboardingStep)
  const setOnboardingStep = useStore((s) => s.setOnboardingStep)
  const setOnboardingWorkspace = useStore((s) => s.setOnboardingWorkspace)
  const upsertWorkspace = useStore((s) => s.upsertWorkspace)
  const onboardingWorkspace = useStore((s) => s.onboardingWorkspace)
  const workspaces = useStore((s) => s.workspaces)
  const workspace = onboardingWorkspace ? workspaces[onboardingWorkspace] ?? null : null
  const [step, setStepState] = useState(() =>
    Math.max(0, Math.min(steps.length - 1, persistedStep)),
  )
  // The harness the Agents step set up, so the first run starts on the one
  // that is actually logged in. Empty until a setup shell exits cleanly.
  const [setUpHarness, setSetUpHarness] = useState('')

  const setStep = (next: number) => {
    setStepState(next)
    setOnboardingStep(next)
  }

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

        {step === 0 && (
          <LinkStep
            client={client}
            onNext={(nextStep) => setStep(nextStep)}
          />
        )}
        {step === 1 && (
          <WorkspaceStep
            client={client}
            caps={caps}
            onNext={(w) => {
              upsertWorkspace(w)
              setOnboardingWorkspace(w.id)
              setStep(2)
            }}
          />
        )}
        {step === 2 && (
          <RepoStep
            client={client}
            caps={caps}
            workspace={workspace}
            onNext={() => setStep(3)}
          />
        )}
        {step === 3 && (
          <AgentsStep
            client={client}
            caps={caps}
            workspace={workspace}
            onReady={setSetUpHarness}
            onNext={() => setStep(4)}
          />
        )}
        {step === 4 && (
          <FirstRunStep
            client={client}
            workspace={workspace}
            defaultHarness={setUpHarness}
            onBackToWorkspace={() => setStep(1)}
          />
        )}

        {step > 0 && (
          <button
            type="button"
            className="text-xs text-muted-foreground underline-offset-2 hover:underline"
            onClick={() => setStep(step - 1)}
          >
            Back
          </button>
        )}
      </div>
    </div>
  )
}

registerRoute('onboarding', OnboardingRoute)
