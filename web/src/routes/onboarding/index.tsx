
import { Check } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { cn, focusRing } from '@/lib/utils'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import { GitIdentityStep } from '@/routes/onboarding/git-identity-step'
import { WorkspaceRepository } from '@/components/workspace-repository'
import {
  FirstRunStep,
  LinkStep,
  WorkspaceStep,
} from '@/routes/onboarding/steps'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { onboardingStepIndex, onboardingSteps } from '@/store/ui'

const chip = 'rounded-sm border px-2 py-1.5'

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
  // Repository and every later step need the workspace the wizard settled on.
  const firstStep = caps.hasLocal('link.status') ? 0 : onboardingStepIndex('Git identity')
  const reachable = (index: number) =>
    Math.max(firstStep, index >= onboardingStepIndex('Repository') && !workspace
      ? onboardingStepIndex('Workspace')
      : index)
  const [step, setStepState] = useState(() => reachable(onboardingStepIndex(persistedStep)))
  // Every step already reached stays reachable: a jump backwards must not
  // strand the member on a step whose own Back is gone.
  const furthest = Math.max(step, reachable(onboardingStepIndex(persistedFurthest)))
  const currentStep = reachable(step)
  const current = onboardingSteps[currentStep]
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
    currentStep > firstStep ? (
      <Button
        type="button"
        size="sm"
        variant="secondary"
        onClick={() => (subStep ? setSubStep('') : setStep(currentStep - 1))}
      >
        Back
      </Button>
    ) : null


  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Onboarding" subtitle={current} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <main className="mx-auto flex min-w-0 w-full max-w-6xl flex-col gap-4 px-4 py-4 sm:px-6">
          <div className="flex flex-wrap items-end justify-between gap-3">
            <div>
              <p className="text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
                Setup path
              </p>
              <p className="mt-1 text-sm text-muted-foreground">
                Step {currentStep - firstStep + 1} of {onboardingSteps.length - firstStep}
              </p>
            </div>
            <p className="text-xs text-muted-foreground">
              Your progress is saved as you move through the wizard.
            </p>
          </div>
          <ol
            aria-label="Steps"
            className="grid grid-cols-2 gap-px border-y border-border/70 bg-border/70 sm:grid-cols-2 lg:grid-cols-3"
          >
            {onboardingSteps.slice(firstStep).map((label, visibleIndex) => {
              const i = visibleIndex + firstStep
              return <li key={label} className="min-w-0" aria-current={i === currentStep ? 'step' : undefined}>
                {i !== currentStep && i <= furthest ? (
                  <button
                    type="button"
                    aria-label={`${visibleIndex + 1}. ${label}, visited - go to this step`}
                    className={cn(
                      focusRing,
                      chip,
                      'flex min-w-0 w-full items-center gap-2 border-0 bg-card px-2 py-1.5 text-left text-xs transition-colors hover:bg-toolbar-hover',
                    )}
                    onClick={() => setStep(i)}
                  >
                    <span className="flex size-5 items-center justify-center rounded-full bg-primary/10 text-primary">
                      <Check className="size-3" aria-hidden />
                    </span>
                    <span className="min-w-0 flex-1 truncate">
                      <span aria-hidden="true" className="mr-1 text-xs text-muted-foreground">{visibleIndex + 1}</span>
                      {label}
                    </span>
                  </button>
                ) : (
                  <span
                    className={cn(
                      'flex min-w-0 w-full items-center gap-2 border-l-2 bg-card px-2 py-1.5 text-xs',
                      i === currentStep
                        ? 'border-primary/50 bg-primary/10 text-foreground'
                        : 'text-muted-foreground',
                    )}
                  >
                    <span
                      className={cn(
                        'flex size-5 items-center justify-center rounded-full text-xs',
                        i === currentStep
                          ? 'bg-primary text-primary-foreground'
                          : 'bg-muted',
                      )}
                    >
                      <span aria-hidden="true">{visibleIndex + 1}</span>
                    </span>
                    <span className="min-w-0 truncate">{label}</span>
                  </span>
                )}
              </li>
            })}
          </ol>

          <div className="min-w-0">
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
                onNext={(w, source) => {
                  upsertWorkspace(w)
                  setOnboardingWorkspace(w.id)
                  setActiveWorkspace(w.id)
                  const previous = useStore.getState().onboardingRepo
                  useStore.getState().setOnboardingSource(source ?? (previous?.workspace === w.id ? 'local' : 'remote'))
                  setStep(onboardingStepIndex('Repository'))
                }}
              />
            )}
            {current === 'Repository' && (
              workspace && <WorkspaceRepository
                key={workspace.id}
                client={client}
                caps={caps}
                workspace={workspace}
                back={back}
                initialLocal={useStore.getState().onboardingSource === 'local'}
                onNext={() => setStep(onboardingStepIndex('Agents'))}
                onLocalChange={(local) => useStore.getState().setOnboardingSource(local ? 'local' : 'remote')}
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
                onBackToRepository={() => setStep(onboardingStepIndex('Repository'))}
              />
            )}
          </div>
        </main>
      </div>
    </div>


  )
}

registerRoute('onboarding', OnboardingRoute)
