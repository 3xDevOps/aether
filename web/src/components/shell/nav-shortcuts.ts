import { useEffect, useRef } from 'react'
import { canLaunch } from '@/lib/commands'
import { keyboardBusy } from '@/lib/keys'
import { runTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { useCapability, useSelfRole } from '@/store/hooks'

/** How long a `g` prefix waits for the key that completes it, in ms. */
const chordWindow = 1500

/** The shell's single-key navigation. Unmodified keys, so every one of them
 * stands down while the keyboard belongs to something else. */
export function useNavShortcuts(): void {
  const launchable = canLaunch({ cap: useCapability(), role: useSelfRole() })
  // Outside the effect: hydration flips `launchable`, which rebuilds the
  // listener, and a `g` pressed just before that must survive it.
  const goToUntil = useRef(0)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Pressing a modifier is not pressing a key: it fires its own keydown,
      // and reaching for Shift must not cancel a chord halfway through.
      if (['Shift', 'Control', 'Alt', 'Meta'].includes(e.key)) return
      // Read and clear first: a `g` abandoned in a terminal or under a dialog
      // must not complete minutes later on the next key the shell does see.
      const goingTo = Date.now() < goToUntil.current
      goToUntil.current = 0
      if (e.defaultPrevented) return
      if (e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return
      if (keyboardBusy(e)) return

      const s = useStore.getState()
      switch (e.key.toLowerCase()) {
        case 'g':
          goToUntil.current = Date.now() + chordWindow
          return
        case 'b':
          if (!goingTo) return
          e.preventDefault()
          s.navigate('board')
          return
        case 'l':
          if (!goingTo) return
          e.preventDefault()
          s.navigate('overview')
          return
        case 'n':
          if (goingTo || !launchable) return
          e.preventDefault()
          s.openPaletteDialog('launch')
          return
        case 'escape':
          // A pending chord swallows it: the reader is cancelling the chord,
          // not asking to leave the run. Anywhere but a run, Esc is not ours.
          if (goingTo) return
          if (!runTabs.some((t) => t.route === s.route.name)) return
          e.preventDefault()
          s.navigate('board')
          return
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [launchable])
}
