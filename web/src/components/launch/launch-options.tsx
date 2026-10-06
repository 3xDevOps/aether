import { GitBranch } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Member, Workspace } from '@/lib/types'

export function AccountOptions({
  label,
  accounts,
  ownAccountID,
  account,
  onChange,
}: {
  label: string
  accounts: Member[]
  ownAccountID: string
  account: string
  onChange: (account: string) => void
}) {
  const accountName = (member: Member) => `${member.display_name}${member.id === ownAccountID ? ' (you)' : ' (shared)'}`
  const chosen = accounts.find((member) => member.id === account)
  const shared = account !== '' && account !== ownAccountID
  return (
    <Collapsible defaultOpen={shared}>
      <CollapsibleTrigger>
        <span className="font-medium">Options</span>
        {chosen && <span className="min-w-0 truncate text-muted">· {label}: {accountName(chosen)}</span>}
      </CollapsibleTrigger>
      <CollapsibleContent className="flex flex-col gap-1 pt-2 pl-5">
        <Label htmlFor="launch-account">{label}</Label>
        <Select value={account} onValueChange={onChange}>
          <SelectTrigger id="launch-account">
            <SelectValue placeholder="Choose an account" />
          </SelectTrigger>
          <SelectContent>
            {accounts.map((member) => (
              <SelectItem key={member.id} value={member.id}>
                {accountName(member)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-ui-sm text-muted">
          {shared
            ? `Uses ${chosen?.display_name ?? account}'s agent login and vendor quota in your own environment, with your GitHub login. You remain its owner and actor.`
            : 'A teammate who shares their agent account appears here.'}
        </p>
      </CollapsibleContent>
    </Collapsible>
  )
}

export function WorkspaceLine({ workspace }: { workspace: Workspace | undefined }) {
  return (
    <p aria-label="Target workspace" className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
      {workspace ? (
        <>
          <GitBranch aria-hidden className="size-3.5 shrink-0" />
          <span className="min-w-0 truncate">
            <span className="text-text">{workspace.name}</span> from <code className="font-code">{workspace.base_branch}</code>
          </span>
        </>
      ) : (
        'Pick a workspace in the sidebar first.'
      )}
    </p>
  )
}
