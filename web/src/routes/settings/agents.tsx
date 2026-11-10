import { AgentList } from '@/components/agents/agent-list'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import type { Api } from '@/lib/api'
import { useAgentList } from '@/routes/agents/use-agents'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'

export function AgentsSection({ client }: { client: Api }) {
  const navigate = useStore((s) => s.navigate)
  const { agents, error, reload } = useAgentList(client)
  const openAgents = () => navigate('agents')
  return (
    <>
      <SettingsSection title="Agents" id="agents-list">
        <SettingRow
          label="Install, sign in and update"
          help="Installing, signing in and updating happen on the Agents page."
          control={<Button size="sm" variant="secondary" onClick={openAgents}>Open Agents</Button>}
        />
      </SettingsSection>
      {error && <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={reload}>Retry</Button>}>{error}</Callout>}
      {agents && agents.length > 0 && <AgentList agents={agents} onSetUp={openAgents} />}
    </>
  )
}
