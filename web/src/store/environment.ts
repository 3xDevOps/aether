import type { EnvironmentStatus, ManifestItem } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/**
 * One workspace's latest environment build: primed by the review gate's
 * approve and then driven by the environment.build event stream. The
 * approved pair rides along because env.status never returns the
 * Dockerfile, so a verification failure can seed its repair scan only
 * from here.
 */
export interface EnvBuild {
  version: number
  status: EnvironmentStatus
  /** Explains a failed status, from the event frame. */
  detail?: string
  /** The approved pair behind the build; absent when the build was only
   * ever seen through events, which leaves repair unavailable. */
  harness?: string
  dockerfile?: string
  manifest?: ManifestItem[]
  /** Which scan path produced the pair, so a repair approval saves with
   * the same source; the repository folder rides along for repo pairs so
   * the repair's refine run reads the same repository. */
  source?: 'mirror' | 'repo'
  repoPath?: string
}

export interface EnvironmentSlice {
  /** The latest known build per workspace id. Session-scoped: nothing
   * here persists, a reload simply shows no banner. */
  envBuilds: Record<string, EnvBuild>
  /** Records an approved build before env.build is called, so the banner
   * is up before the first event frame can land. */
  startEnvBuild: (workspaceID: string, build: EnvBuild) => void
  /** Applies one environment.build event frame. Frames about a version
   * older than the tracked one are stale and ignored; frames that change
   * nothing coarse (a build-output line) write no state. */
  applyEnvBuild: (
    workspaceID: string,
    frame: { version: number; status: EnvironmentStatus; detail?: string },
  ) => void
  /** Forgets a workspace's build - the "keep the standard environment"
   * dismissal. The workspace image already is the fallback. */
  clearEnvBuild: (workspaceID: string) => void
}

export const createEnvironmentSlice: SliceCreator<EnvironmentSlice> = (
  set,
) => ({
  envBuilds: {},
  startEnvBuild: (workspaceID, build) =>
    set((s) => ({ envBuilds: { ...s.envBuilds, [workspaceID]: build } })),
  applyEnvBuild: (workspaceID, frame) =>
    set((s) => {
      const current = s.envBuilds[workspaceID]
      if (current && frame.version < current.version) return s
      const sameVersion = current !== undefined && current.version === frame.version
      const detail = frame.detail ?? (sameVersion ? current.detail : undefined)
      if (sameVersion && current.status === frame.status && current.detail === detail) {
        return s
      }
      // A frame for a newer version replaces the entry outright: the kept
      // pair belongs to the version it was approved with, nothing else.
      const next: EnvBuild = sameVersion
        ? { ...current, status: frame.status, detail }
        : { version: frame.version, status: frame.status, detail }
      return { envBuilds: { ...s.envBuilds, [workspaceID]: next } }
    }),
  clearEnvBuild: (workspaceID) =>
    set((s) => {
      const next = { ...s.envBuilds }
      delete next[workspaceID]
      return { envBuilds: next }
    }),
})
