import type { LaunchDraft } from '@/components/launch/draft'

type PopoutMessage =
  | { source: 'aether-launch-popout'; type: 'discarded'; draftID: string }
  | { source: 'aether-launch-popout'; type: 'launched'; draftID: string; runID: string }

const popouts = new WeakSet<object>()

export function launchPopoutURL(href: string, draft: LaunchDraft): string {
  const url = new URL(href)
  for (const key of ['run', 'view', 'agent', 'repo', 'page', 'id', 'watch', 'entry', 'assistant', 'card', 'new', 'prompt', 'popout']) {
    url.searchParams.delete(key)
  }
  url.searchParams.set('new', '1')
  url.searchParams.set('popout', '1')
  url.hash = new URLSearchParams([['launch-draft', JSON.stringify(draft)]]).toString()
  return url.toString()
}

export function openLaunchPopout(href: string, draft: LaunchDraft): boolean {
  const popup = window.open(launchPopoutURL(href, draft), '_blank', 'popup,width=1280,height=840')
  if (!popup) return false
  popouts.add(popup)
  return true
}

export function sendLaunchPopoutDiscarded(draftID: string): void {
  const opener = window.opener
  if (opener && !opener.closed) {
    opener.postMessage({ source: 'aether-launch-popout', type: 'discarded', draftID }, window.location.origin)
  }
}

export function sendLaunchPopoutLaunched(runID: string, draftID: string): boolean {
  const opener = window.opener
  if (!opener || opener.closed) return false
  opener.postMessage({ source: 'aether-launch-popout', type: 'launched', runID, draftID }, window.location.origin)
  window.close()
  return true
}

export function receiveLaunchPopoutMessage(event: MessageEvent): PopoutMessage | null {
  if (event.origin !== window.location.origin || !event.source || typeof event.source !== 'object' || !popouts.has(event.source)) return null
  const value = event.data as { source?: unknown; type?: unknown; draftID?: unknown; runID?: unknown } | null
  if (!value || value.source !== 'aether-launch-popout') return null
  if (value.type === 'discarded' && typeof value.draftID === 'string') return value as PopoutMessage
  if (value.type === 'launched' && typeof value.draftID === 'string' && typeof value.runID === 'string' && value.runID !== '') {
    popouts.delete(event.source)
    return value as PopoutMessage
  }
  return null
}
