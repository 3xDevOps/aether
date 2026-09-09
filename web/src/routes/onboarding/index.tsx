// The onboarding wizard: the quickstart's most error-prone stretch - link,
// git identity, workspace, repo remote, agents, first run - as six steps. It
// exists only where the gateway has this machine's SSH identity and
// filesystem, so the whole route gates on the link.status local verb; a
// remote gateway gets an empty state, not a broken wizard. Step and
// workspace choices persist so a reload resumes where the user left off; the
// step persists by name, so adding one does not move anyone mid-wizard.
//
// Navigation is two levels and nothing more: a step index, and a sub-screen
// name owned by whichever step has one. Back closes the sub-screen first and
// only then leaves the step, so a step's own screens never fall through to
// the previous step. The wizard owns the Back button; every step renders it
// in its own action row.

import { Check } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { cn, focusRing } from '@/lib/utils'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import { GitIdentityStep } from '@/routes/onboarding/git-identity-step'
import { RepoStep } from '@/routes/onboarding/repo-step'
import {
  FirstRunStep,
  LinkStep,
  WorkspaceStep,
} from '@/routes/onboarding/steps'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { onboardingStepIndex, onboardingSteps } from '@/store/ui'

/** One step's marker in the header: reached, current, or still ahead. */
const chip = 'rounded-sm border px-1.5 py-0.5'

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
  const persistedFurthest = useStore((s) => s.onboardingFurthest)
  // Repository and everything past it need the workspace the wizard settled
  // on; without one there is nothing to resume into, and nothing further
  // back to jump forward to either.
  const reachable = (index: number) =>
    index >= onboardingStepIndex('Repository') && !onboardingWorkspace
      ? onboardingStepIndex('Workspace')
      : index
  const [step, setStepState] = useState(() => reachable(onboardingStepIndex(persistedStep)))
  // Every step already reached stays reachable: a jump backwards must not
  // strand the member on a step whose own Back is gone.
  const furthest = Math.max(step, reachable(onboardingStepIndex(persistedFurthest)))
  const current = onboardingSteps[step]
  // The harness the Agents step set up, so the first run starts on the one
  // that is actually logged in. Empty until a setup shell exits cleanly.
  const [setUpHarness, setSetUpHarness] = useState('')
  // The open sub-screen of the current step; empty is the step's own screen.
  const [subStep, setSubStep] = useState('')

  const setStep = (next: number) => {
    setStepState(next)
    setSubStep('')
    setOnboardingStep(onboardingSteps[next])
  }

  const back =
    step > 0 ? (
      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={() => (subStep ? setSubStep('') : setStep(step - 1))}
      >
        Back
      </Button>
    ) : null

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
      <ViewHeader title="Onboarding" subtitle={current} />
      <div className="flex-1 space-y-4 overflow-y-auto p-4">
        <ol
          aria-label="Steps"
          className="sticky top-0 z-10 -mx-4 -mt-4 flex flex-wrap gap-2 bg-background px-4 pb-2 pt-4 text-xs"
        >
          {onboardingSteps.map((label, i) => (
            <li key={label} aria-current={i === step ? 'step' : undefined}>
              {i !== step && i <= furthest ? (
                <button
                  type="button"
                  aria-label={`${i + 1}. ${label}, done - go to this step`}
                  className={cn(focusRing, chip, 'flex items-center gap-1 hover:bg-accent')}
                  onClick={() => setStep(i)}
                >
                  <Check className="size-3" aria-hidden />
                  {i + 1}. {label}
                </button>
              ) : (
                <span
                  className={`${chip} block ${
                    i === step ? 'font-medium' : 'text-muted-foreground'
                  }`}
                >
                  {i + 1}. {label}
                </span>
              )}
            </li>
          ))}
        </ol>

        {current === 'Link' && (
          <LinkStep
            client={client}
            onNext={(nextStep) => setStep(nextStep)}
          />
        )}
        {current === 'Git identity' && (
          <GitIdentityStep
            client={client}
            caps={caps}
            back={back}
            onNext={() => setStep(onboardingStepIndex('Workspace'))}
          />
        )}
        {current === 'Workspace' && (
          <WorkspaceStep
            client={client}
            caps={caps}
            back={back}
            onNext={(w) => {
              upsertWorkspace(w)
              setOnboardingWorkspace(w.id)
              setActiveWorkspace(w.id)
              setStep(onboardingStepIndex('Repository'))
            }}
          />
        )}
        {current === 'Repository' && (
          <RepoStep
            client={client}
            caps={caps}
            workspace={workspace}
            back={back}
            onNext={() => setStep(onboardingStepIndex('Agents'))}
          />
        )}
        {current === 'Agents' && (
          <AgentsStep
            client={client}
            caps={caps}
            workspace={workspace}
            back={back}
            setup={subStep}
            onSetup={setSubStep}
            onReady={setSetUpHarness}
            onNext={() => setStep(onboardingStepIndex('First run'))}
          />
        )}
        {current === 'First run' && (
          <FirstRunStep
            client={client}
            workspace={workspace}
            back={back}
            defaultHarness={setUpHarness}
            onBackToWorkspace={() => setStep(onboardingStepIndex('Workspace'))}
            onBackToAgents={() => setStep(onboardingStepIndex('Agents'))}
          />
        )}
      </div>
    </div>
  )
}

registerRoute('onboarding', OnboardingRoute)
