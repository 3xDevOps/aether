import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { DevicesPanel } from '@/routes/devices'
import { InvitationsSection, InviteDialog } from '@/routes/members/invitations'
import { Roster } from '@/routes/members/roster'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'

type Tab = 'members' | 'devices'

export function MembersPage({ tab, client = api }: { tab: Tab; client?: Api }) {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const navigate = useStore((s) => s.navigate)
  const [inviting, setInviting] = useState(false)
  const [invited, setInvited] = useState(0)
  const canInvite = isAdmin && (caps.hasMethod('member.invite') || caps.hasMethod('member.invitation.create'))
  const showDevices = caps.hasMethod('member.device.list')

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader
        title={tab === 'devices' ? 'Devices' : 'Members'}
        actions={canInvite && <Button size="sm" onClick={() => setInviting(true)}>Invite…</Button>}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <Tabs value={tab} onValueChange={(value) => navigate(value)}>
          <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-6 px-4 py-4 sm:px-6">
            {showDevices && (
              <div className="border-b border-seam">
                <TabsList aria-label="Members and devices">
                  <TabsTrigger value="members">Members</TabsTrigger>
                  <TabsTrigger value="devices">Devices</TabsTrigger>
                </TabsList>
              </div>
            )}
            <TabsContent value="members" className="flex min-w-0 flex-col gap-8">
              <Roster client={client} />
              {isAdmin && caps.hasMethod('member.invitation.list') && <InvitationsSection client={client} version={invited} />}
            </TabsContent>
            {showDevices && (
              <TabsContent value="devices">
                <DevicesPanel client={client} />
              </TabsContent>
            )}
          </div>
        </Tabs>
      </div>
      {inviting && <InviteDialog client={client} onClose={() => setInviting(false)} onInvited={() => setInvited((n) => n + 1)} />}
    </div>
  )
}

export function MembersRoute({ client }: RouteProps & { client?: Api }) {
  const tab = useStore((s) => (s.route.name === 'devices' ? 'devices' : 'members'))
  return <MembersPage tab={tab} client={client} />
}

registerRoute('members', MembersRoute)
registerRoute('devices', MembersRoute)
