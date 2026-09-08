// The onboarding wizard: the quickstart's most error-prone stretch - link,
// git identity, workspace, repo remote, agents, first run - as six steps. It
// exists only where the gateway has this machine's SSH identity and
// filesystem, so the whole route gates on the link.status local verb; a
// remote gateway gets an empty state, not a broken wizard. Step and
// workspace choices persist so a reload resumes where the user left off.
//
// Navigation is two levels and nothing more: a step index, and a sub-screen
// name owned by whichever step has one. Back closes the sub-screen first and
// only then leaves the step, so a step's own screens never fall through to
// the previous step.

import { useState } from 'react'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import { GitIdentityStep } from '@/routes/onboarding/git-identity-step'
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
  'Git identity',
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
  const setActiveWorkspace = useStore((s) => s.setActiveWorkspace)
  const onboardingWorkspace = useStore((s) => s.onboardingWorkspace)
  const workspaces = useStore((s) => s.workspaces)
  const workspace = onboardingWorkspace ? workspaces[onboardingWorkspace] ?? null : null
  const [step, setStepState] = useState(() =>
    Math.max(
      0,
      Math.min(
        steps.length - 1,
        persistedStep >= 3 && !onboardingWorkspace ? 2 : persistedStep,
      ),
    ),
  )
  // The harness the Agents step set up, so the first run starts on the one
  // that is actually logged in. Empty until a setup shell exits cleanly.
  const [setUpHarness, setSetUpHarness] = useState('')
  // The open sub-screen of the current step; empty is the step's own screen.
  const [subStep, setSubStep] = useState('')

  const setStep = (next: number) => {
    setStepState(next)
    setSubStep('')
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
          <GitIdentityStep client={client} caps={caps} onNext={() => setStep(2)} />
        )}
        {step === 2 && (
          <WorkspaceStep
            client={client}
            caps={caps}
            onNext={(w) => {
              upsertWorkspace(w)
              setOnboardingWorkspace(w.id)
              setActiveWorkspace(w.id)
              setStep(3)
            }}
          />
        )}
        {step === 3 && (
          <RepoStep
            client={client}
            caps={caps}
            workspace={workspace}
            onNext={() => setStep(4)}
          />
        )}
        {step === 4 && (
          <AgentsStep
            client={client}
            caps={caps}
            workspace={workspace}
            setup={subStep}
            onSetup={setSubStep}
            onReady={setSetUpHarness}
            onNext={() => setStep(5)}
          />
        )}
        {step === 5 && (
          <FirstRunStep
            client={client}
            workspace={workspace}
            defaultHarness={setUpHarness}
            onBackToWorkspace={() => setStep(2)}
          />
        )}

        {step > 0 && (
          <button
            type="button"
            className="text-xs text-muted-foreground underline-offset-2 hover:underline"
            onClick={() => (subStep ? setSubStep('') : setStep(step - 1))}
          >
            Back
          </button>
        )}
      </div>
    </div>
  )
}

registerRoute('onboarding', OnboardingRoute)
