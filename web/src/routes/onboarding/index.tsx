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
      <div className="flex h-full min-w-0 flex-col">
        <ViewHeader title="Onboarding" />
        <div className="flex flex-1 items-center justify-center p-4">
          <div className="w-full max-w-lg border border-border/70 bg-card px-4 py-4 text-left">
            <p className="text-base font-medium">Onboarding needs a local gateway</p>
            <p className="mt-2 text-sm leading-6 text-muted-foreground">
              This dashboard is served by the Aether server, which holds no SSH
              identity of yours and can reach no repository on your computer.
              Onboarding runs in the desktop app or `aether gui` there, where
              the gateway has both.
            </p>
          </div>
        </div>
      </div>
    )
  }

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
                Step {step + 1} of {onboardingSteps.length}
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
            {onboardingSteps.map((label, i) => (
              <li key={label} className="min-w-0" aria-current={i === step ? 'step' : undefined}>
                {i !== step && i <= furthest ? (
                  <button
                    type="button"
                    aria-label={`${i + 1}. ${label}, done - go to this step`}
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
                      <span aria-hidden="true" className="mr-1 text-xs text-muted-foreground">{i + 1}</span>
                      {label}
                    </span>
                  </button>
                ) : (
                  <span
                    className={cn(
                      'flex min-w-0 w-full items-center gap-2 border-l-2 bg-card px-2 py-1.5 text-xs',
                      i === step
                        ? 'border-primary/50 bg-primary/10 text-foreground'
                        : 'text-muted-foreground',
                    )}
                  >
                    <span
                      className={cn(
                        'flex size-5 items-center justify-center rounded-full text-xs',
                        i === step
                          ? 'bg-primary text-primary-foreground'
                          : 'bg-muted',
                      )}
                    >
                      <span aria-hidden="true">{i + 1}</span>
                    </span>
                    <span className="min-w-0 truncate">{label}</span>
                  </span>
                )}
              </li>
            ))}
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
        </main>
      </div>
    </div>


  )
}

registerRoute('onboarding', OnboardingRoute)
