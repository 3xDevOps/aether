import { useEffect } from 'react'
import { toast } from 'sonner'
import { canLaunch } from '@/lib/commands'
import { useStore } from '@/store'
import { useCapability, useSelfRole } from '@/store/hooks'

/**
 * Share to Aether opens `?new=1&prompt=<text>`. The launch form opens with
 * that task and waits for Launch; the two parameters leave the address so a
 * reload does not open it again. Another open form is finished first, so
 * the share never replaces its draft.
 */
export function useSharedPrompt() {
  const hydrated = useStore((s) => s.hydrated)
  const free = useStore((s) => s.paletteDialog === null || s.paletteDialog === 'launch')
  const role = useSelfRole()
  const cap = useCapability()
  const ready = hydrated && role !== null && free
  useEffect(() => {
    if (!ready) return
    const url = new URL(window.location.href)
    if (url.searchParams.get('new') !== '1') return
    const prompt = url.searchParams.get('prompt') ?? ''
    url.searchParams.delete('new')
    url.searchParams.delete('prompt')
    window.history.replaceState(window.history.state, '', url.toString())
    if (canLaunch({ cap, role })) useStore.getState().openLaunchWithTask(prompt)
    else if (!cap.hasMethod('run.launch')) toast.error('Shared text not opened: this gateway does not offer run.launch, so it cannot start sessions.')
    else toast.error(`Shared text not opened: a ${role} cannot launch sessions.`)
  }, [ready, cap, role])
}
