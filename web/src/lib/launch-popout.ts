import { parseLaunchDraft, type LaunchDraft } from '@/components/launch/draft'

type PopoutMessage =
  | { source: 'aether-launch-popout'; type: 'discarded'; draftID: string }
  | { source: 'aether-launch-popout'; type: 'returned'; draft: LaunchDraft }
  | { source: 'aether-launch-popout'; type: 'launched'; draftID: string; runID: string }

const popouts = new WeakSet<object>()
let activePopout: WindowProxy | null = null

export function launchPopoutURL(href: string, draft: LaunchDraft, token?: string | null): string {
  const url = new URL(href)
  for (const key of ['run', 'view', 'agent', 'repo', 'page', 'id', 'watch', 'entry', 'assistant', 'card', 'new', 'prompt', 'popout']) {
    url.searchParams.delete(key)
  }
  url.searchParams.set('new', '1')
  url.searchParams.set('popout', '1')
  if (token) url.searchParams.set('token', token)
  url.hash = new URLSearchParams([['launch-draft', JSON.stringify(draft)]]).toString()
  return url.toString()
}

export function openLaunchPopout(href: string, draft: LaunchDraft, token?: string | null): boolean {
  const popup = window.open(launchPopoutURL(href, draft, token), '_blank', 'popup,width=1280,height=840')
  if (!popup) return false
  activePopout = popup
  popouts.add(popup)
  return true
}

export function focusLaunchPopout(): boolean {
  if (!activePopout || activePopout.closed) {
    activePopout = null
    return false
  }
  activePopout.focus()
  return true
}

export function sendLaunchPopoutDiscarded(draftID: string): void {
  const opener = window.opener
  if (opener && !opener.closed) {
    opener.postMessage({ source: 'aether-launch-popout', type: 'discarded', draftID }, window.location.origin)
  }
  window.close()
}

export function sendLaunchPopoutReturned(draft: LaunchDraft): void {
  const opener = window.opener
  if (opener && !opener.closed) {
    opener.postMessage({ source: 'aether-launch-popout', type: 'returned', draft }, window.location.origin)
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
  const value = event.data as { source?: unknown; type?: unknown; draft?: unknown; draftID?: unknown; runID?: unknown } | null
  if (!value || value.source !== 'aether-launch-popout') return null
  if (value.type === 'returned' && typeof value.draft === 'object' && value.draft !== null) {
    const draft = parseLaunchDraft(JSON.stringify(value.draft))
    if (!draft) return null
    popouts.delete(event.source)
    if (activePopout === event.source) activePopout = null
    return { source: 'aether-launch-popout', type: 'returned', draft }
  }
  if (value.type === 'discarded' && typeof value.draftID === 'string' && value.draftID !== '') {
    popouts.delete(event.source)
    if (activePopout === event.source) activePopout = null
    return value as PopoutMessage
  }
  if (value.type === 'launched' && typeof value.draftID === 'string' && value.draftID !== '' && typeof value.runID === 'string' && value.runID !== '') {
    popouts.delete(event.source)
    if (activePopout === event.source) activePopout = null
    return value as PopoutMessage
  }
  return null
}
