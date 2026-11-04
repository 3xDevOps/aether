import { useEffect } from 'react'
import { toast } from 'sonner'
import { canLaunch } from '@/lib/commands'
import { takeSharedPrompt } from '@/lib/url-state'
import { useStore } from '@/store'
import { useCapability, useSelfRole } from '@/store/hooks'

/** The most a share may carry; the Android share target sends no more. */
const sharedLimit = 8000

/**
 * Opens New session with the text shared at load (`takeSharedPrompt`) once
 * the dashboard has hydrated and no other form is open, so the share never
 * replaces another form's draft. The form waits for Launch.
 */
export function useSharedPrompt() {
  const hydrated = useStore((s) => s.hydrated)
  const free = useStore((s) => s.paletteDialog === null || s.paletteDialog === 'launch')
  const role = useSelfRole()
  const cap = useCapability()
  const ready = hydrated && role !== null && free
  useEffect(() => {
    if (!ready) return
    const text = takeSharedPrompt()
    if (text === null) return
    if (!canLaunch({ cap, role })) {
      toast.error(cap.hasMethod('run.launch')
        ? `Shared text not opened: a ${role} cannot launch sessions.`
        : 'Shared text not opened: this gateway does not offer run.launch, so it cannot start sessions.')
      return
    }
    if (text.length > sharedLimit) toast.warning(`Shared text cut to its first ${sharedLimit} characters, of ${text.length}.`)
    useStore.getState().openLaunchWithTask(text.slice(0, sharedLimit))
  }, [ready, cap, role])
}
