import type { SliceCreator } from '@/store/slice'

/**
 * The header frame a shell client sends on /ws/shell, mirroring the
 * protocol's WorkspaceShellRequest: which workspace, and what the shell is
 * for. The mode strings are the domain WorkspaceShellMode values.
 */
export interface WorkspaceShellReq {
  workspace: { id?: string; name?: string }
  mode: 'bootstrap-tools' | 'harness-login' | 'agent-setup'
  harness?: string
  tui_args?: string[]
  headless_args?: string[]
  resume?: boolean
  reset?: boolean
}

/**
 * The one shell the app can show at a time. A view asks for a shell by
 * describing it; the shell surface renders whatever request is open. A
 * shell is a one-shot session, so closing forgets the request entirely.
 */
export interface ShellSlice {
  shellRequest: WorkspaceShellReq | null
  openShell: (req: WorkspaceShellReq) => void
  closeShell: () => void
}

export const createShellSlice: SliceCreator<ShellSlice> = (set) => ({
  shellRequest: null,
  openShell: (req) => set({ shellRequest: req }),
  closeShell: () => set({ shellRequest: null }),
})
