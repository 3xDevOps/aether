import { type ReactNode, useState } from 'react'
import { defaultMode, enhancedSupported, label, loginFor, ready } from '@/components/agents/agent-copy'
import { ModeComparison, type SetupMode } from '@/components/agents/mode-comparison'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code, CodeBlock } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Spinner } from '@/components/ui/spinner'
import { StatusDot } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentInfo, AgentInstallResult } from '@/lib/types'
import { TerminalDock } from '@/routes/environment/terminal-dock'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

function SetupStep({ number, title, children }: { number: number; title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-3">
      <h3 className="flex items-center gap-2 text-ui font-medium text-text">
        <span aria-hidden className="grid size-5 shrink-0 place-items-center rounded-full bg-chrome text-ui-xs text-muted tabular-nums">
          {number}
        </span>
        {title}
      </h3>
      <div className="flex min-w-0 flex-col gap-3 pl-7 max-sm:pl-0">{children}</div>
    </section>
  )
}

function Fact({ ok, children }: { ok: boolean; children: ReactNode }) {
  return (
    <li className="flex items-center gap-1.5 text-ui text-text">
      <StatusDot tone={ok ? 'done' : 'needs-you'} />
      {children}
    </li>
  )
}

export function AgentSetup({
  agent: initial,
  client,
  onDone,
}: {
  agent: AgentInfo
  client: Api
  onDone: (agent: AgentInfo) => void
}) {
  const caps = useCapability()
  const rememberLaunch = useStore((s) => s.rememberLaunch)
  const [agent, setAgent] = useState(initial)
  const [mode, setMode] = useState<SetupMode>(() => {
    const remembered = defaultMode(initial, useStore.getState().launchDefaults[initial.name]?.mode)
    return remembered === 'acp' ? 'acp' : 'tui'
  })
  const [installing, setInstalling] = useState(false)
  const [result, setResult] = useState<AgentInstallResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)
  const [checked, setChecked] = useState(false)
  const name = label(agent)
  const enhanced = mode === 'acp'
  const shipped = agent.source === 'shipped'
  const canInstall = shipped && !!agent.install_script && caps.hasMethod('agent.install')
  const needsInstall = agent.installed !== true || (enhanced && agent.enhanced_installed !== true)
  const hasTerminal = caps.hasWS('terminal')
  const login = loginFor(agent)
  const typedInstall = enhanced ? agent.enhanced_install_script ?? agent.install_script : agent.install_script

  const check = async () => {
    setChecking(true)
    setError(null)
    try {
      const listed = (await client.agentList()).find((item) => item.name === agent.name)
      if (!listed) throw new Error(`agent.list no longer lists ${agent.name}`)
      setAgent(listed)
      setChecked(true)
    } catch (err) {
      setError(message(err))
    } finally {
      setChecking(false)
    }
  }

  const install = async () => {
    setInstalling(true)
    setError(null)
    setResult(null)
    try {
      const answer = await client.agentInstall(agent.name, enhanced)
      setResult(answer)
      setAgent((current) => ({ ...current, installed: answer.installed, enhanced_installed: answer.enhanced_installed }))
      if (!answer.error) await check()
    } catch (err) {
      setError(message(err))
    } finally {
      setInstalling(false)
    }
  }

  const finish = () => {
    rememberLaunch(agent.name, enhanced && agent.enhanced_installed ? 'acp' : 'tui')
    onDone(agent)
  }

  const installed = agent.installed === true && (!enhanced || agent.enhanced_installed === true)

  return (
    <section aria-label={`Set up ${name}`} className="flex min-w-0 flex-col gap-6">
      <h2 className="flex items-center gap-2 text-title text-text">
        <AgentGlyph agent={agent.glyph ?? agent.name} colored className="size-5" />
        Set up {name}
      </h2>

      <SetupStep number={1} title="Choose how runs show it">
        <ModeComparison agent={agent} value={mode} onChange={setMode} />
        {!enhancedSupported(agent) && <p className="text-ui-sm text-muted">Runs of {name} use Standard.</p>}
      </SetupStep>

      <SetupStep number={2} title={canInstall ? 'Install and log in' : 'Install and log in in your environment'}>
        {canInstall ? (
          <>
            <p className="text-ui text-text">
              {needsInstall
                ? `Installs ${name}${enhanced && agent.enhanced === 'adapter' ? ' and its Enhanced adapter' : ''} in your environment, the container Aether keeps for you on the server. Every workspace uses it.`
                : `${name} is installed in your environment${enhanced ? ' with Enhanced' : ''}.`}
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <Button variant={needsInstall ? 'primary' : 'secondary'} disabled={installing} onClick={() => void install()}>
                {needsInstall ? `Install ${name}` : 'Install again'}
              </Button>
              {installing && (
                <span className="flex items-center gap-1.5 text-ui-sm text-muted">
                  <Spinner label="Installing" />
                  Installing, this can take a few minutes…
                </span>
              )}
            </div>
          </>
        ) : (
          <p className="text-ui text-text">
            {typedInstall
              ? 'The install command is typed into your environment terminal below. Press Enter to run it.'
              : `Install the ${agent.name} executable into ~/.local/bin from your environment terminal below.`}
          </p>
        )}
        {result?.error && (
          <Callout tone="failed" role="alert" title="Install failed">
            {result.error}
          </Callout>
        )}
        {result && (
          <Collapsible defaultOpen={!!result.error}>
            <CollapsibleTrigger>Install output</CollapsibleTrigger>
            <CollapsibleContent className="pt-2">
              <div className="max-h-64 overflow-y-auto">
                <CodeBlock className="whitespace-pre-wrap break-words">{result.log_tail.trim() || 'The command printed nothing.'}</CodeBlock>
              </div>
            </CollapsibleContent>
          </Collapsible>
        )}
        {(agent.installed === true || !canInstall) && (
          hasTerminal ? (
            <>
              {login && (
                <p className="text-ui text-text">
                  Log in once: <Code>{login.command}</Code> is typed into your environment terminal. {login.hint}
                </p>
              )}
              <TerminalDock client={client} openOnMount initialLine={canInstall ? login?.command : typedInstall} />
            </>
          ) : (
            <>
              <p className="text-ui text-text">Open your environment terminal from a computer with the CLI, then {canInstall ? `log in. ${login?.hint ?? ''}` : 'install the agent:'}</p>
              <CodeBlock>{['aether terminal', canInstall ? login?.command : typedInstall].filter(Boolean).join('\n')}</CodeBlock>
            </>
          )
        )}
      </SetupStep>

      <SetupStep number={3} title="Check">
        {checked && (
          <ul aria-label={`${name} status`} className="flex flex-col gap-1">
            <Fact ok={agent.installed === true}>{agent.installed === true ? 'Installed' : 'Not installed'}</Fact>
            {enhanced && <Fact ok={agent.enhanced_installed === true}>{agent.enhanced_installed ? 'Enhanced installed' : 'Enhanced not installed'}</Fact>}
            {agent.login_found !== undefined && <Fact ok={agent.login_found}>{agent.login_found ? 'Login found' : 'No login found'}</Fact>}
          </ul>
        )}
        {checked && agent.login_found === false && agent.installed === true && (
          <p className="text-ui-sm text-muted">Aether looks for the agent's login file in your environment; log in above, then check again.</p>
        )}
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        <div className="flex flex-wrap items-center gap-2">
          {checked && installed && ready(agent) ? (
            <Button onClick={finish}>Done</Button>
          ) : (
            <Button variant="secondary" disabled={checking} onClick={() => void check()}>
              {checking ? 'Checking…' : checked ? 'Check again' : 'Check'}
            </Button>
          )}
        </div>
      </SetupStep>
    </section>
  )
}
