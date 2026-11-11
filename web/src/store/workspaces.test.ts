import { useStore } from '@/store'
import { otherWorkspace, run, runRecords, workspace } from '@/test/fixtures'

describe('setWorkspaces', () => {
  it('drops the runs of a workspace the caller no longer reaches', () => {
    const kept = run({ id: 'run_kept', workspace_id: workspace.id })
    const lost = run({ id: 'run_lost', workspace_id: otherWorkspace.id })
    useStore.setState({
      workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
      deletedWorkspaceIDs: new Set(),
      runs: runRecords(kept, lost),
    })
    useStore.getState().setWorkspaces([workspace])
    expect(Object.keys(useStore.getState().runs)).toEqual(['run_kept'])
  })
})
