import { AgentList } from '@/components/agents/agent-list'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import type { Api } from '@/lib/api'
import { useAgentList } from '@/routes/agents/use-agents'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'

export function AgentsSection({ client }: { client: Api }) {
  const navigate = useStore((s) => s.navigate)
  const { agents, error, reload } = useAgentList(client, true)
  return (
    <>
      <SettingsSection title="Sign-in" id="agents-list" scope="Your account">
        <SettingRow
          label="Install, sign in and update"
          help="Do these on the Agents page."
          control={<Button size="sm" variant="secondary" onClick={() => navigate('agents')}>Open Agents</Button>}
        />
      </SettingsSection>
      {agents === null && error === null && <p role="status" className="text-ui text-muted">Loading agents…</p>}
      {error && <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={reload}>Retry</Button>}>{error}</Callout>}
      {agents?.length === 0 && <p className="text-ui text-muted">This server lists no agents.</p>}
      {agents && agents.length > 0 && <AgentList agents={agents} />}
    </>
  )
}
