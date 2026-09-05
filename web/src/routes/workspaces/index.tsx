
import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import {
  EnvironmentChoice,
  type EnvironmentValue,
} from '@/components/environment-choice'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { Workspace, WorkspaceImageResult } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

export function WorkspacesRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  const navigate = useStore((s) => s.navigate)
  const standardImage = useStore((s) => s.info?.standard_image)
  const [workspaces, setWorkspaces] = useState<Workspace[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loading = useDelayed(workspaces === null && error === null)

  const refetch = useCallback(async () => {
    try {
      setWorkspaces(await client.workspaceListFull())
    } catch (err) {
      setError(message(err))
    }
  }, [client])

  useEffect(() => {
    void refetch()
  }, [refetch])

  return (
    <div className="flex h-full flex-col">
      <ViewHeader
        title="Workspaces"
        subtitle={workspaces ? `${workspaces.length} total` : undefined}
      />
      <div className="flex-1 space-y-6 overflow-y-auto p-4">
        {caps.hasMethod('workspace.add') && (
          <AddForm client={client} onAdded={() => void refetch()} />
        )}

        {loading && <Skeleton className="h-24 w-full" />}
        {error && <p className="text-xs text-state-failed">{error}</p>}

        <ul className="space-y-4">
          {(workspaces ?? []).map((workspace) => (
            <li key={workspace.id} className="space-y-3 rounded-md border bg-card p-3">
              <div className="flex flex-wrap items-center gap-3">
                <span className="min-w-0 flex-1 truncate text-sm font-medium">
                  {workspace.name}
                </span>
                <span className="font-mono text-xs text-muted-foreground">
                  {workspace.base_branch}
                </span>
                <span className="text-xs text-muted-foreground">
                  {workspace.steer_others === 'admins_only'
                    ? 'admins steer others'
                    : 'everyone with steer'}
                </span>
                <span className="text-xs text-muted-foreground">
                  created {timeAgo(workspace.created_at)}
                </span>
                {/* Opening a workspace is also how a member changes scope:
                    every other surface follows activeWorkspace. */}
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => navigate('workspace', { workspaceId: workspace.id })}
                >
                  Open
                </Button>
              </div>
              {caps.hasMethod('workspace.image') && (
                <WorkspaceImageRow
                  client={client}
                  workspace={workspace}
                  standardImage={standardImage}
                />
              )}
            </li>
          ))}
        </ul>
        {workspaces?.length === 0 && (
          <p className="text-sm text-muted-foreground">No workspaces yet.</p>
        )}
      </div>
    </div>
  )
}

function imageRepository(ref: string): string {
  const digest = ref.indexOf('@')
  const named = digest >= 0 ? ref.slice(0, digest) : ref
  const slash = named.lastIndexOf('/')
  const colon = named.lastIndexOf(':')
  return colon > slash ? named.slice(0, colon) : named
}

function imageTag(ref: string): string {
  const colon = ref.lastIndexOf(':')
  return colon > ref.lastIndexOf('/') ? ref.slice(colon + 1) : ''
}

function WorkspaceImageRow({
  client,
  workspace,
  standardImage,
}: {
  client: Api
  workspace: Workspace
  standardImage?: string
}) {
  const [image, setImage] = useState<string | null>(null)
  const [draft, setDraft] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    client
      .workspaceImage(workspace.id)
      .then((result) => {
        if (cancelled) return
        setImage(result.image)
        setDraft(result.image)
      })
      .catch((err) => {
        if (!cancelled) setError(message(err))
      })
    return () => {
      cancelled = true
    }
  }, [client, workspace.id])

  const save = async (ref: string) => {
    setBusy(true)
    setError(null)
    try {
      const result: WorkspaceImageResult = await client.workspaceImage(
        workspace.id,
        ref,
      )
      setImage(result.image)
      setDraft(result.image)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const updateTag =
    image &&
    standardImage &&
    imageRepository(image) === imageRepository(standardImage) &&
    imageTag(image) !== imageTag(standardImage)
      ? imageTag(standardImage)
      : null

  return (
    <div className="space-y-2 border-t pt-3">
      <dl className="space-y-1 text-sm">
        <div className="flex gap-2">
          <dt className="text-muted-foreground">Image</dt>
          <dd className="font-mono">{image === null ? 'Loading...' : image || 'none'}</dd>
        </div>
      </dl>
      {error && <p className="text-xs text-state-failed">{error}</p>}
      {image !== null && (
        <div className="flex flex-wrap items-end gap-2">
          <label className="min-w-0 flex-1 space-y-1 text-xs text-muted-foreground">
            Image reference
            <input
              className={field}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
            />
          </label>
          <Button
            size="sm"
            variant="outline"
            disabled={busy || !draft.trim()}
            onClick={() => void save(draft.trim())}
          >
            Save
          </Button>
          {updateTag && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => {
                if (standardImage) void save(standardImage)
              }}
            >
              Update to {updateTag}
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

function AddForm({ client, onAdded }: { client: Api; onAdded: () => void }) {
  const [name, setName] = useState('')
  const [baseBranch, setBaseBranch] = useState('main')
  const [environment, setEnvironment] = useState<EnvironmentValue | null>(null)
  const [busy, setBusy] = useState(false)
  // Bumped after a successful add to remount the environment cards back to
  // their default selection, the way the old image field was cleared.
  const [epoch, setEpoch] = useState(0)

  const add = async () => {
    setBusy(true)
    try {
      if (!environment) throw new Error('choose an environment first')
      await client.workspaceAdd({
        name: name.trim(),
        base_branch: baseBranch.trim(),
        environment,
      })
      setName('')
      setEpoch((e) => e + 1)
      onAdded()
      toast.success('Workspace added')
    } catch (err) {
      toast.error(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      className="space-y-3"
      aria-label="Add workspace"
      onSubmit={(e) => {
        e.preventDefault()
        void add()
      }}
    >
      <div className="flex items-end gap-3">
        <label className="flex-1 space-y-1 text-sm">
          Name
          <input
            className={field}
            value={name}
            placeholder="team"
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label className="flex-1 space-y-1 text-sm">
          Base branch
          <input
            className={field}
            value={baseBranch}
            onChange={(e) => setBaseBranch(e.target.value)}
          />
        </label>
      </div>
      <EnvironmentChoice key={epoch} onChange={setEnvironment} />
      <Button
        type="submit"
        disabled={busy || !name.trim() || !baseBranch.trim() || !environment}
      >
        Add
      </Button>
    </form>
  )
}

registerRoute('workspaces', WorkspacesRoute)
