import { ProfileImport } from '@/components/profile-import'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

export function ConfigurationRoute({ client = api }: RouteProps & { client?: Api }) {
  const navigate = useStore((s) => s.navigate)
  const caps = useCapability()
  const canImport = caps.hasMethod('config.roots') && caps.hasMethod('config.import')

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader
        title="Configuration"
        subtitle="your persistent remote home"
        actions={
          canImport && (
            <Button size="sm" variant="outline" onClick={() => navigate('files')}>
              Open remote files
            </Button>
          )
        }
      />
      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-[1000px] min-w-0 flex-col gap-4 p-4 sm:p-6">
          {canImport ? (
            <>
              <p className="text-[13px] text-muted-foreground">
                Import a local configuration directory into your own persistent remote home.
                You can repeat the import whenever you choose, without a workspace or onboarding.
                This is an explicit copy, not a watcher or automatic sync: later local changes
                stay local until you import again. Use Files to inspect and edit the remote copy.
              </p>
              <ProfileImport client={client} />
            </>
          ) : (
            <p className="text-[13px] text-muted-foreground">
              This gateway does not support configuration import.
            </p>
          )}
        </div>
      </main>
    </div>
  )
}

registerRoute('configuration', ConfigurationRoute)
