import { useEffect, useRef, useState } from 'react'
import { Check } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { AgentStep } from '@/routes/onboarding/agent-step'
import { ConnectStep } from '@/routes/onboarding/connect-step'
import { FirstRunStep } from '@/routes/onboarding/first-run-step'
import { RepositoryStep } from '@/routes/onboarding/repository-step'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, useHeaderPrimary } from '@/store/hooks'
import { onboardingStepIndex, onboardingSteps } from '@/store/ui'

function StepChips({ first, current, furthest, onJump }: { first: number; current: number; furthest: number; onJump: (step: number) => void }) {
  const list = useRef<HTMLOListElement>(null)
  useEffect(() => {
    list.current?.querySelector('[aria-current="step"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [current])
  return (
    <ol ref={list} aria-label="Steps" className="flex min-w-0 items-center gap-1 overflow-x-auto max-sm:gap-0.5">
      {onboardingSteps.slice(first).map((label, visibleIndex) => {
        const i = visibleIndex + first
        const number = visibleIndex + 1
        const here = i === current
        const reached = i <= furthest
        return (
          <li key={label} aria-current={here ? 'step' : undefined} className="flex shrink-0 items-center gap-1">
            {visibleIndex > 0 && <span aria-hidden className="h-px w-3 bg-seam max-sm:w-1.5" />}
            {!here && reached ? (
              <Button size="sm" variant="ghost" aria-label={`${number}. ${label}, visited - go to this step`} onClick={() => onJump(i)}>
                <Check className="text-state-done" />
                {label}
              </Button>
            ) : (
              <span className={cn('flex h-6 items-center gap-1.5 px-2 text-ui-sm coarse:h-11', here ? 'font-medium text-text' : 'text-muted')}>
                <span
                  aria-hidden
                  className={cn(
                    'grid size-4 place-items-center rounded-full text-ui-xs tabular-nums',
                    here ? 'bg-accent text-on-accent' : 'border border-seam',
                  )}
                >
                  {number}
                </span>
                <span>{label}</span>
              </span>
            )}
          </li>
        )
      })}
    </ol>
  )
}

export function OnboardingRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  useHeaderPrimary(true)
  const persistedStep = useStore((s) => s.onboardingStep)
  const persistedFurthest = useStore((s) => s.onboardingFurthest)
  const setOnboardingStep = useStore((s) => s.setOnboardingStep)
  const onboardingWorkspace = useStore((s) => s.onboardingWorkspace)
  const onboardingSource = useStore((s) => s.onboardingSource)
  const workspace = useStore((s) => (onboardingWorkspace ? s.workspaces[onboardingWorkspace] ?? null : null))
  const firstStep = caps.hasLocal('link.status') ? 0 : onboardingStepIndex('Repository')
  const [step, setStepState] = useState(() => Math.max(firstStep, onboardingStepIndex(persistedStep)))
  const currentStep = Math.max(firstStep, step)
  // Every step already reached stays reachable: a jump backwards must not
  // strand the member on a step whose own Back is gone.
  const furthest = Math.max(currentStep, onboardingStepIndex(persistedFurthest))
  const current = onboardingSteps[currentStep]
  // The open sub-screen of the current step; empty is the step's own screen.
  const [subStep, setSubStep] = useState('')

  const setStep = (next: number) => {
    setStepState(next)
    setSubStep('')
    setOnboardingStep(onboardingSteps[next])
  }

  const back =
    currentStep > firstStep || subStep ? (
      <Button variant="secondary" onClick={() => (subStep ? setSubStep('') : setStep(currentStep - 1))}>
        Back
      </Button>
    ) : null

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader
        title="Onboarding"
        titleAdornment={<StepChips first={firstStep} current={currentStep} furthest={furthest} onJump={setStep} />}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col px-4 pt-6 sm:px-6">
          {current === 'Connect' && (
            <ConnectStep client={client} caps={caps} onNext={() => setStep(onboardingStepIndex('Repository'))} />
          )}
          {current === 'Repository' && (
            <RepositoryStep
              client={client}
              caps={caps}
              workspace={workspace}
              local={onboardingSource === 'local'}
              back={back}
              onChoose={(w, source) => {
                const state = useStore.getState()
                state.upsertWorkspace(w)
                state.setOnboardingWorkspace(w.id)
                state.setActiveWorkspace(w.id)
                state.setOnboardingSource(source ?? (state.onboardingRepo?.workspace === w.id ? 'local' : 'remote'))
              }}
              onLocalChange={(local) => useStore.getState().setOnboardingSource(local ? 'local' : 'remote')}
              onNext={() => setStep(onboardingStepIndex('Agent'))}
            />
          )}
          {current === 'Agent' && (
            <AgentStep
              client={client}
              caps={caps}
              back={back}
              setup={subStep}
              onSetup={setSubStep}
              onNext={() => setStep(onboardingStepIndex('First run'))}
            />
          )}
          {current === 'First run' && (
            <FirstRunStep
              client={client}
              workspace={workspace}
              back={back}
              onBackToRepository={() => setStep(onboardingStepIndex('Repository'))}
              onBackToAgent={() => setStep(onboardingStepIndex('Agent'))}
            />
          )}
        </div>
      </div>
    </div>
  )
}

registerRoute('onboarding', OnboardingRoute)
