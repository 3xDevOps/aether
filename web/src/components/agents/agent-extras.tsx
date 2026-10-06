import type { ReactNode } from 'react'
import { ProfileImport } from '@/components/profile-import'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import type { Api } from '@/lib/api'
import type { GitHubConnectResult } from '@/lib/types'
import { GitHubSection } from '@/routes/onboarding/github-connect'
import { GitIdentityForm } from '@/routes/onboarding/git-identity'
import type { Capability } from '@/store/hooks'

function Extra({ title, summary, open, children }: { title: string; summary: string; open?: boolean; children: ReactNode }) {
  return (
    <div className="border-t border-seam py-1">
      <Collapsible defaultOpen={open}>
        <CollapsibleTrigger>
          <span className="font-medium">{title}</span>
          <span className="min-w-0 truncate text-muted">{summary}</span>
        </CollapsibleTrigger>
        <CollapsibleContent className="pt-2 pb-3 pl-5">{children}</CollapsibleContent>
      </Collapsible>
    </div>
  )
}

export function AgentExtras({
  client,
  caps,
  identity = false,
  github,
  onConnectGitHub,
}: {
  client: Api
  caps: Capability
  identity?: boolean
  github: GitHubConnectResult | null
  onConnectGitHub: () => void
}) {
  return (
    <div className="flex min-w-0 flex-col">
      {identity && caps.hasMethod('member.git') && (
        <Extra title="Git identity" summary="The author of your agents' commits">
          <GitIdentityForm client={client} caps={caps} />
        </Extra>
      )}
      <Extra title="GitHub" summary="Push branches and open pull requests as you" open={github !== null}>
        {caps.hasMethod('github.connect') && caps.hasMethod('github.probe') ? (
          <GitHubSection connection={github} onOpen={onConnectGitHub} />
        ) : (
          <p className="text-ui text-text">
            Log in with <code className="font-code">gh auth login</code> in <code className="font-code">aether terminal</code>, then run{' '}
            <code className="font-code">aether github connect</code> from your linked computer.
          </p>
        )}
      </Extra>
      {caps.hasMethod('config.roots') && caps.hasMethod('config.import') && (
        <Extra title="Agent config files" summary="Copy ~/.claude, ~/.codex and similar to the server">
          <ProfileImport client={client} />
        </Extra>
      )}
    </div>
  )
}
