// The sync overlay's registrations. The badge goes into the run card's
// meta slot instead of the card file; the panel itself is rendered by the
// settings route's live-overlay card.

import { registerSlot } from '@/components/slots'
import { SyncBadge } from '@/routes/run-sync/sync-panel'

registerSlot('card:meta', 'sync', SyncBadge)

export { SyncPanel } from '@/routes/run-sync/sync-panel'
