import { canLaunch } from '@/lib/commands'
import { useKeybindings } from '@/lib/keybindings'
import { surfaces } from '@/lib/surfaces'
import { useStore } from '@/store'
import { useCapability, useSelfRole } from '@/store/hooks'

/** The `g` sequences whose destination a gateway may not serve. */
const goTo = {
  'go-missions': 'missions',
  'go-activity': 'timeline',
  'go-files': 'files',
  'go-agents': 'agents',
  'go-settings': 'settings',
} as const

/** The shell's single-key navigation. A destination the gateway does not
 * serve has no handler, so its keys do nothing. */
export function useNavShortcuts(): void {
  const cap = useCapability()
  const launchable = canLaunch({ cap, role: useSelfRole() })
  const navigate = useStore((s) => s.navigate)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const available = new Set(surfaces(cap).map((surface) => surface.name))

  useKeybindings('global', {
    'go-board': () => navigate('board'),
    'go-overview': () => navigate('overview'),
    ...Object.fromEntries(
      Object.entries(goTo)
        .filter(([, route]) => available.has(route))
        .map(([id, route]) => [id, () => navigate(route)]),
    ),
    ...(launchable && { launch: () => openDialog('launch') }),
  })
}
