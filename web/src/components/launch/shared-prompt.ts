import { useEffect } from 'react'
import { toast } from 'sonner'
import { parseLaunchDraft } from '@/components/launch/draft'
import { canLaunch } from '@/lib/commands'
import { hasLaunchPopoutRequest, takeLaunchPopoutRequest, takeSharedPrompt } from '@/lib/url-state'
import { useStore } from '@/store'
import { useCapability, useSelfRole } from '@/store/hooks'

/** The most a share may carry; the Android share target sends no more. */
const sharedLimit = 8000

/**
 * Opens New session with the text shared at load (`takeSharedPrompt`) once
 * the server has answered and no other form is open, so the share never
 * replaces another form's draft. The form waits for Launch.
 */
export function useSharedPrompt() {
  // A saved snapshot hydrates before the server answers; its role may be out of date.
  const hydrated = useStore((s) => s.hydrated && s.staleSince === null)
  const free = useStore((s) => s.paletteDialog === null && s.launchDraft === null)
  const popoutPending = hasLaunchPopoutRequest()
  const role = useSelfRole()
  const cap = useCapability()
  const ready = hydrated && role !== null && (free || popoutPending)
  useEffect(() => {
    if (!ready) return
    const popout = takeLaunchPopoutRequest()
    if (popout) {
      if (!canLaunch({ cap, role })) {
        toast.error(cap.hasMethod('run.launch')
          ? `New session not opened: a ${role} cannot launch sessions.`
          : 'New session not opened: this gateway does not offer run.launch, so it cannot start sessions.')
        return
      }
      const draft = popout.draft === null ? null : parseLaunchDraft(popout.draft)
      if (draft) useStore.getState().setLaunchDraft(draft)
      else if (popout.draft !== null) toast.error('The popped-out New session settings could not be restored.')
      if (useStore.getState().launchDraft) useStore.getState().openPaletteDialog('launch')
      else useStore.getState().openLaunchWithTask('')
      return
    }
    const text = takeSharedPrompt()
    if (text === null) return
    if (!canLaunch({ cap, role })) {
      toast.error(cap.hasMethod('run.launch')
        ? `Shared text not opened: a ${role} cannot launch sessions.`
        : 'Shared text not opened: this gateway does not offer run.launch, so it cannot start sessions.')
      return
    }
    const characters = [...text]
    if (characters.length > sharedLimit) toast.warning(`Shared text cut to its first ${sharedLimit} characters, of ${characters.length}.`)
    useStore.getState().openLaunchWithTask(characters.slice(0, sharedLimit).join(''))
  }, [ready, cap, role])
}
