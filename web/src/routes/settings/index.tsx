import { type ReactNode, useEffect, useId, useMemo, useRef } from 'react'
import { useAssistantAvailable } from '@/components/assistant/button'
import { ChevronLeft } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { surfaces } from '@/lib/surfaces'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { AssistantSection } from '@/routes/settings/assistant'
import { ConnectorsSection } from '@/routes/settings/connectors'
import { GeneralSections } from '@/routes/settings/general'
import { GitHubSection } from '@/routes/settings/github'
import { SettingsNav } from '@/routes/settings/nav'
import { PushSection, SlackSection } from '@/routes/settings/notifications'
import { type CategoryID, categoryOf, visibleCategories, visibleEntries } from '@/routes/settings/registry'
import { RemoteAccessSection } from '@/routes/settings/remote-access'
import { ServerSection } from '@/routes/settings/server'
import { ThisComputerSections } from '@/routes/settings/this-computer'
import { UsageSection } from '@/routes/settings/usage'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'

const bodies: Record<CategoryID, (client: Api) => ReactNode> = {
  general: () => <GeneralSections />,
  connections: (client) => <><ConnectorsSection client={client} /><GitHubSection client={client} /></>,
  notifications: (client) => <><PushSection client={client} /><SlackSection client={client} /></>,
  assistant: (client) => <AssistantSection client={client} />,
  usage: (client) => <UsageSection client={client} />,
  'this-computer': (client) => <ThisComputerSections client={client} />,
  server: (client) => <ServerSection client={client} />,
  'remote-access': (client) => <RemoteAccessSection client={client} />,
}

const highlightMs = 1500

function reveal(target: HTMLElement) {
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  target.scrollIntoView?.({ block: 'start', behavior: reduced ? 'auto' : 'smooth' })
  const control = target.querySelector<HTMLElement>('button:not(:disabled), input:not(:disabled), a[href], textarea:not(:disabled)')
  ;(control ?? target.closest('section')?.querySelector<HTMLElement>('h3'))?.focus({ preventScroll: true })
  target.setAttribute('data-highlight', '')
  setTimeout(() => target.removeAttribute('data-highlight'), highlightMs)
}

export function SettingsRoute({ params, client = api }: RouteProps & { client?: Api }) {
  const phone = useIsMobile()
  const cap = useCapability()
  const admin = useIsAdmin()
  const assistant = useAssistantAvailable()
  const member = useStore((s) => s.info?.member !== undefined)
  const workspace = useStore((s) => s.activeWorkspace !== '')
  const navigate = useStore((s) => s.navigate)
  const hydrated = useStore((s) => s.hydrated)
  const onboarding = surfaces(cap).some((surface) => surface.name === 'onboarding')
  const shown = useMemo(() => visibleEntries({ cap, admin, assistant, onboarding, member, workspace }), [cap, admin, assistant, onboarding, member, workspace])
  const visible = visibleCategories(shown)
  const target = params.section
  const linked = target ? categoryOf(target, shown) : undefined
  const current = linked ?? (phone ? undefined : visible[0])
  const heading = useRef<HTMLHeadingElement>(null)
  const left = useRef<string | null>(null)
  const headingID = useId()

  useEffect(() => {
    if (!target || !linked) return
    if (target === linked.id) {
      heading.current?.focus()
      return
    }
    const element = document.getElementById(target)
    if (element) {
      reveal(element)
      return
    }
    // A row whose data is still loading mounts later.
    const observer = new MutationObserver(() => {
      const mounted = document.getElementById(target)
      if (!mounted) return
      observer.disconnect()
      reveal(mounted)
    })
    observer.observe(document.body, { childList: true, subtree: true })
    return () => observer.disconnect()
  }, [params, linked?.id])

  useEffect(() => {
    if (current) {
      left.current = current.id
      return
    }
    if (!left.current) return
    document.querySelector<HTMLElement>(`[data-settings-category="${left.current}"]`)?.focus()
    left.current = null
  }, [current])

  const notice = hydrated && target && !linked && <Callout tone="neutral">That setting is not available here.</Callout>
  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Settings" />
      <div className="flex min-h-0 flex-1">
        {!(phone && current) && (
          <div className={phone ? 'min-w-0 flex-1 overflow-y-auto' : 'w-56 shrink-0 overflow-y-auto border-r border-seam'}>
            {phone && notice && <div className="px-3 pt-3">{notice}</div>}
            <SettingsNav shown={shown} visible={visible} current={current?.id} phone={phone} />
          </div>
        )}
        {current && (
          <section aria-labelledby={headingID} className="min-w-0 flex-1 overflow-y-auto">
            <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-8 px-4 py-6 sm:px-6">
              <div className="flex min-w-0 flex-col items-start gap-1">
                {phone && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="-ml-2 mb-2"
                    onClick={() => navigate('settings')}
                  >
                    <ChevronLeft />Settings
                  </Button>
                )}
                {notice}
                <h2 ref={heading} id={headingID} tabIndex={-1} className="text-title text-text outline-none">{current.title}</h2>
                <p className="text-ui text-muted">{current.purpose}</p>
              </div>
              {bodies[current.id](client)}
            </div>
          </section>
        )}
      </div>
    </div>
  )
}

registerRoute('settings', SettingsRoute)
