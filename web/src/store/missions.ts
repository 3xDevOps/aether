import type {
  Mission,
  MissionAttempt,
  MissionPlanReview,
  MissionQuestion,
  MissionScopeDiagnostic,
  MissionSubmission,
  MissionTask,
} from '@/lib/types'
import type { SliceCreator } from '@/store/slice'
export interface MissionDetail {
  mission: Mission
  tasks: MissionTask[]
  attempts: MissionAttempt[]
  submissions: MissionSubmission[]
  diagnostics: MissionScopeDiagnostic[]
  questions: MissionQuestion[]
  plan_reviews: MissionPlanReview[]
}

export interface MissionsSlice {
  missions: Record<string, Mission>
  missionDetails: Record<string, MissionDetail>
  missionNextCursor: string | null
  missionLoading: boolean
  missionError: string | null
  setMissions: (missions: Mission[], nextCursor?: string, append?: boolean) => void
  upsertMission: (mission: Mission) => void
  setMissionDetail: (detail: MissionDetail) => void
  setMissionLoading: (loading: boolean) => void
  setMissionError: (error: string | null) => void
}

export const createMissionsSlice: SliceCreator<MissionsSlice> = (set) => ({
  missions: {},
  missionDetails: {},
  missionNextCursor: null,
  missionLoading: false,
  missionError: null,
  setMissions: (missions, nextCursor, append = false) =>
    set((state) => ({
      missions: append
        ? { ...state.missions, ...Object.fromEntries(missions.map((mission) => [mission.id, mission])) }
        : Object.fromEntries(missions.map((mission) => [mission.id, mission])),
      missionNextCursor: nextCursor ?? null,
      missionError: null,
      // The show projection is independently authoritative and must survive
      // list refreshes, including a page that does not contain its mission.
      missionDetails: state.missionDetails,
    })),
  upsertMission: (mission) =>
    set((state) => ({ missions: { ...state.missions, [mission.id]: mission } })),
  setMissionDetail: (detail) =>
    set((state) => ({
      missions: { ...state.missions, [detail.mission.id]: detail.mission },
      missionDetails: { ...state.missionDetails, [detail.mission.id]: detail },
      missionError: null,
    })),
  setMissionLoading: (missionLoading) => set({ missionLoading }),
  setMissionError: (missionError) => set({ missionError, missionLoading: false }),
})
