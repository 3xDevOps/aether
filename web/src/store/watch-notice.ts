import { toast } from 'sonner'
import type { Api } from '@/lib/api'
import type { Event, WatchChangedPayload } from '@/lib/types'
import { linkTarget } from '@/lib/watches'
import type { RootStore } from '@/store'
import { liveWindowMs, serverOffset } from '@/store/assistant-notice'
import { readWatch } from '@/store/watches'

export function openLink(store: RootStore, link: string): void {
  const target = linkTarget(link)
  if ('href' in target) window.open(target.href, '_blank', 'noopener,noreferrer')
  else store.getState().navigate(target.route.name, target.route.params)
}

/**
 * Re-reads the watch the event names. A new notice toasts with its title and
 * body, unless the event is a replayed catch-up, which updates the list only.
 */
export async function applyWatchChanged(store: RootStore, client: Api, ev: Event): Promise<void> {
  const p = ev.payload as WatchChangedPayload
  if (store.getState().info?.member.id !== p.member_id) return
  const result = await readWatch(store, client, p.watch_id).catch(() => null)
  if (!result || !p.notification_id) return
  if (Date.now() + serverOffset(store) - Date.parse(ev.time) > liveWindowMs) return
  const notice = result.notifications.find((n) => n.id === p.notification_id)
  if (!notice) return
  toast(notice.title, {
    description: notice.body || undefined,
    ...(notice.link ? { action: { label: 'Open', onClick: () => openLink(store, notice.link) } } : {}),
  })
}
