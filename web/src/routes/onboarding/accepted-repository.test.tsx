import { fireEvent, render, screen } from '@testing-library/react'
import type { Workspace } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { useStore } from '@/store'
import { fakeApi, serverInfo, workspace } from '@/test/fixtures'

const accepted = { ...workspace, id: 'ws_github', name: 'GitHub repository' }
vi.mock('@/components/workspace-create', () => ({
  WorkspaceCreate: ({ onCreated }: { onCreated: (workspace: Workspace, source: 'remote', accepted?: boolean) => void }) => (
    <button onClick={() => onCreated(accepted, 'remote', true)}>Accept GitHub revision</button>
  ),
}))

it('advances an accepted repository directly to Agent while retaining the selected workspace and remote source', async () => {
  useStore.setState({
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events'] },
    route: { name: 'onboarding', params: {} },
    workspaces: {},
    activeWorkspace: '',
    onboardingStep: 'Repository',
    onboardingFurthest: 'Repository',
    onboardingWorkspace: '',
    onboardingSource: 'local',
    onboardingRepo: null,
  })
  const client = fakeApi({
    workspaceListFull: vi.fn(async () => []),
    githubOAuthStatus: vi.fn(async () => ({ state: 'connected' as const, login: 'octocat' })),
  })
  render(<OnboardingRoute params={{}} client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Accept GitHub revision' }))
  await screen.findByRole('heading', { name: 'Set up an agent' })
  expect(useStore.getState().onboardingWorkspace).toBe(accepted.id)
  expect(useStore.getState().activeWorkspace).toBe(accepted.id)
  expect(useStore.getState().workspaces[accepted.id]).toEqual(accepted)
  expect(useStore.getState().onboardingSource).toBe('remote')
  expect(useStore.getState().onboardingStep).toBe('Agent')
  expect(useStore.getState().route.name).toBe('onboarding')
  await screen.findByText(/octocat/)
  expect(client.githubOAuthStart).not.toHaveBeenCalled()
  expect(screen.queryByRole('button', { name: "I've logged in" })).toBeNull()
})
