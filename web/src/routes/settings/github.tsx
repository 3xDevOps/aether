import { useEffect, useRef, useState } from 'react'
import { GitHubConnection } from '@/components/github-connection'
import { Button } from '@/components/ui/button'
import type { Api } from '@/lib/api'
import { GitHubRepositoryDialog } from '@/routes/admin-dialogs'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'

export function GitHubSection({ client }: { client: Api }) {
  const isAdmin = useIsAdmin()
  const caps = useCapability()
  const [adding, setAdding] = useState(false)
  const generation = useRef(0)
  useEffect(() => {
    setAdding(false)
    const unsubscribe = useStore.subscribe((state, previous) => {
      if (state.identityKey === previous.identityKey && state.connectionEpoch === previous.connectionEpoch && state.route === previous.route && state.info?.member?.role === previous.info?.member?.role) return
      generation.current += 1
      setAdding(false)
    })
    return () => { generation.current += 1; unsubscribe() }
  }, [client])
  if (!isAdmin) return null
  const version = generation.current
  return (
    <SettingsSection title="GitHub" id="github" scope="Server · admins">
      <SettingRow label="Connection">{!adding && <GitHubConnection client={client} />}</SettingRow>
      {caps.hasMethod('workspace.import') && (
        <SettingRow
          label="Repositories"
          help="Import a GitHub repository as a workspace."
          control={<Button size="sm" variant="secondary" onClick={() => setAdding(true)}>Add repository</Button>}
        />
      )}
      {adding && <GitHubRepositoryDialog client={client} onClose={() => setAdding(false)} onCreated={(workspace) => {
        if (version !== generation.current) return
        const state = useStore.getState()
        state.upsertWorkspace(workspace)
        state.setActiveWorkspace(workspace.id)
        setAdding(false)
        state.navigate('workspace', { workspaceId: workspace.id })
      }} />}
    </SettingsSection>
  )
}
