import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import type { InvitationResult } from '@/lib/types'
import { errorSentence } from '@/lib/format'
import { DevicesPanel } from '@/routes/devices'
import { dismissOnIdentityChange, reopen, toastFailure, useReopenHost } from '@/components/failure'
import { createInvitation, invitee, InvitationsSection, InviteDialog, type InviteDraft } from '@/routes/members/invitations'
import { Roster } from '@/routes/members/roster'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { sameIdentity, useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'

type Tab = 'members' | 'devices'

interface Inviting {
  draft?: InviteDraft
  error?: string
  pending?: boolean
  created?: InvitationResult
}

export function MembersPage({ tab, client = api }: { tab: Tab; client?: Api }) {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const navigate = useStore((s) => s.navigate)
  const [inviting, setInviting] = useState<Inviting | null>(null)
  const canInvite = isAdmin && (caps.hasMethod('member.invite') || caps.hasMethod('member.invitation.create'))
  const showDevices = caps.hasMethod('member.device.list')

  const open = useRef(inviting)
  const mounted = useRef(true)
  useEffect(() => {
    open.current = inviting
  })
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  useReopenHost('members', setInviting)

  const invite = async (draft: InviteDraft) => {
    const asked: Inviting = { ...open.current, error: undefined, pending: true }
    setInviting(asked)
    const same = sameIdentity()
    // The dialog that asked shows the answer; once it is closed or replaced, a toast does.
    const asking = () => mounted.current && open.current === asked
    try {
      const created = await createInvitation(client, draft)
      if (!same()) return
      if (asking()) return setInviting({ created })
      const shown = toast.success(`Invited ${invitee(created.invitation)}`, {
        action: { label: 'Show link', onClick: () => void (same() && reopen('members', { created }, 'members')) },
      })
      dismissOnIdentityChange(shown, same)
    } catch (cause) {
      if (!same()) return
      const error = errorSentence(cause)
      if (asking()) setInviting({ ...asked, pending: false, error })
      else toastFailure(error, { reopen: () => void (same() && reopen('members', { draft, error }, 'members')), same })
    }
  }

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader
        title={tab === 'devices' ? 'Devices' : 'Members'}
        actions={canInvite && tab === 'members' && <Button size="sm" onClick={() => setInviting({})}>Invite…</Button>}
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
              {isAdmin && caps.hasMethod('member.invitation.list') && <InvitationsSection client={client} />}
            </TabsContent>
            {showDevices && (
              <TabsContent value="devices">
                <DevicesPanel client={client} />
              </TabsContent>
            )}
          </div>
        </Tabs>
      </div>
      {inviting && (
        <InviteDialog
          client={client}
          draft={inviting.draft}
          error={inviting.error}
          pending={inviting.pending}
          created={inviting.created}
          onClose={() => setInviting(null)}
          onInvite={(draft) => void invite(draft)}
        />
      )}
    </div>
  )
}

export function MembersRoute({ client }: RouteProps & { client?: Api }) {
  const tab = useStore((s) => (s.route.name === 'devices' ? 'devices' : 'members'))
  return <MembersPage tab={tab} client={client} />
}

registerRoute('members', MembersRoute)
registerRoute('devices', MembersRoute)
