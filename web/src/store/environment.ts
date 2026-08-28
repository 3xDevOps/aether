import type {
  EnvironmentEditPayload,
  EnvironmentEditStatus,
  EnvironmentStatus,
  ManifestItem,
} from '@/lib/types'
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

/** How many output lines an edit keeps for the "view process" expander;
 * older lines fall off the front. */
export const ENV_EDIT_LINE_CAP = 200

/**
 * One workspace's latest environment edit run: primed by the panel's
 * submit and then driven by the environment.edit event stream.
 */
export interface EnvEdit {
  harness: string
  status: EnvironmentEditStatus
  /** The newest agent output, capped at ENV_EDIT_LINE_CAP. */
  lines: string[]
  /** The proposed definition version, set on `proposed`. */
  version?: number
  /** Explains a failed status, from the event frame. */
  detail?: string
}

/** `proposed` and `failed` end a run; anything after them is a new one. */
function editDone(status: EnvironmentEditStatus): boolean {
  return status === 'proposed' || status === 'failed'
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
  /** The latest known edit run per workspace id. Session-scoped, like
   * envBuilds. */
  envEdits: Record<string, EnvEdit>
  /** Records a submitted edit before env.edit is called, so the in-flight
   * state is up before the first event frame can land. */
  startEnvEdit: (workspaceID: string, harness: string) => void
  /** Applies one environment.edit event frame. A non-terminal frame after
   * a finished run starts a fresh entry - someone began another edit. */
  applyEnvEdit: (workspaceID: string, frame: EnvironmentEditPayload) => void
  /** Forgets a workspace's edit - the review's dismiss. The proposed
   * version, when one exists, stays in the server's history. */
  clearEnvEdit: (workspaceID: string) => void
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
  envEdits: {},
  startEnvEdit: (workspaceID, harness) =>
    set((s) => ({
      envEdits: {
        ...s.envEdits,
        [workspaceID]: { harness, status: 'running', lines: [] },
      },
    })),
  applyEnvEdit: (workspaceID, frame) =>
    set((s) => {
      const current = s.envEdits[workspaceID]
      // A frame past a finished run belongs to a new one: drop the old
      // window and terminal fields rather than mixing two runs.
      const base =
        current && !(editDone(current.status) && !editDone(frame.status))
          ? current
          : { harness: frame.harness, status: frame.status, lines: [] }
      const lines = frame.line
        ? [...base.lines, frame.line].slice(-ENV_EDIT_LINE_CAP)
        : base.lines
      const next: EnvEdit = {
        ...base,
        harness: frame.harness || base.harness,
        status: frame.status,
        lines,
      }
      if (frame.version !== undefined) next.version = frame.version
      if (frame.detail !== undefined) next.detail = frame.detail
      return { envEdits: { ...s.envEdits, [workspaceID]: next } }
    }),
  clearEnvEdit: (workspaceID) =>
    set((s) => {
      const next = { ...s.envEdits }
      delete next[workspaceID]
      return { envEdits: next }
    }),
})
