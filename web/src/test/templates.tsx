import { fireEvent, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { api, type Api } from '@/lib/api'
import type { Schedule } from '@/lib/types'
import { TemplatesRoute } from '@/routes/templates'
import { useStore, type RootState } from '@/store'
import { agentInfo, alice, otherWorkspace, serverInfo, template, workspace } from '@/test/fixtures'

export const schedule: Schedule = {
  id: 'sch_1',
  workspace_id: workspace.id,
  template: template.name,
  cron: '0 3 * * *',
  member_id: alice.id,
  created_at: '2026-08-14T09:05:00Z',
  next_fire_at: '2026-08-23T03:00:00Z',
}

export function seed(extra: Partial<RootState> = {}) {
  vi.spyOn(api, 'agentList').mockResolvedValue([agentInfo({ display_name: 'Claude Code' }), agentInfo({ name: 'copilot', display_name: 'GitHub Copilot CLI' })])
  vi.spyOn(api, 'agentModels').mockResolvedValue({ models: [], source: 'none' })
  useStore.setState({
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    // template.save/delete are gated; an upgraded gateway advertising every
    // method renders them all.
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    hydrationError: null,
    agentModels: {},
    route: { name: 'templates', params: {} },
    // A new identity drops the saves earlier tests left in flight.
    identityKey: crypto.randomUUID(),
    ...extra,
  })
}

// The route as the shell mounts it: its params follow the store's route, which selecting a template changes.
export function Templates({ client }: { client: Api }) {
  const params = useStore((s) => s.route.params)
  return <TemplatesRoute params={params} client={client} />
}

export const detail = (name: string) => screen.getByRole('region', { name, hidden: true })

export async function rowAction(name: string) {
  if (name === 'Edit') {
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }))
    return
  }
  const trigger = await screen.findByRole('button', { name: `More for ${template.name}` })
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  const item = within(await screen.findByRole('menu')).getByRole('menuitem', { name })
  item.focus()
  await userEvent.keyboard('{Enter}')
}
