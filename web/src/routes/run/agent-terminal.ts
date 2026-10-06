import { useEffect, useMemo, useRef, useState } from 'react'
import type { Terminal } from '@xterm/xterm'
import { toast } from 'sonner'
import type { TerminalReadSurface } from '@/components/terminal-pane'
import { useXterm } from '@/components/xterm-host'
import { api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { errorSentence } from '@/lib/format'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { openOAuthLink, remoteOAuthInstructions } from '@/lib/oauth-forward'
import type { RunStatus } from '@/lib/types'
import { getHistoryCache } from '@/routes/terminal/history-cache'
import { useRunTerminalSession } from '@/routes/terminal/session'
import { useTakeover } from '@/routes/terminal/use-takeover'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { allowSessionAutoWrite, requestSessionControl, requestSessionTakeover, sessionStreamOpen, subscribeSession } from '@/store/session-stream'
import type { RunRecord } from '@/store/runs'

/**
 * Statuses a run never leaves. Mirrors `replayableStatus` in
 * internal/sshd/attach.go: past these a run is permanently sessionless, so
 * its refusal is the answer and no retry could help.
 */
export const endedStatuses: readonly RunStatus[] = ['completed', 'merged', 'abandoned', 'failed', 'interrupted']

const startingStatuses: readonly RunStatus[] = ['queued', 'provisioning']

export function useAgentTerminal(run: RunRecord, surfaceShown: boolean) {
  const runID = run.id
  const workspaceID = run.workspace_id
  const steerOthers = useStore((s) => s.workspaces[workspaceID]?.steer_others)
  const capability = useCapability()
  const self = useSelf()
  const identityKey = useStore((s) => s.identityKey)
  const terminalCacheEpoch = useStore((s) => s.terminalCacheEpoch)
  const roomStatus = useStore((s) => s.roomStatus[runID])
  const phone = useMediaQuery(phoneScreen)
  const hasAgentTerminal = run.mode !== 'acp'
  const starting = startingStatuses.includes(run.status)
  const historyCache = useMemo(() => getHistoryCache({
    identityKey,
    epoch: terminalCacheEpoch,
    runID,
    createdAt: run.created_at,
  }), [identityKey, terminalCacheEpoch, runID, run.created_at])
  const beforeDispose = useRef<((terminal: Terminal) => void) | null>(null)
  const historyTools = useRef<TerminalReadSurface | null>(null)
  const [readingHistory, setReadingHistory] = useState(true)

  const steerable = run.status === 'running' || run.status === 'needs-attention'
  const automaticWrite = surfaceShown && !phone && steerable && run.mission_role !== 'worker' && run.member_id === self.id
  const authorityKey = [
    self.id,
    self.role,
    run.member_id,
    run.protected ? 'protected' : 'open',
    workspaceID,
    steerOthers === undefined ? '' : String(steerOthers),
  ].join('\u0000')

  // The session is created after xterm, so input and geometry go through refs.
  const sendRef = useRef<(data: string) => void>(() => {})
  const resizeRef = useRef<(cols: number, rows: number) => void>(() => {})
  const controller = useXterm({
    enabled: hasAgentTerminal,
    follow: phone,
    scrollback: 5000,
    onData: (data) => sendRef.current(data),
    onBeforeDispose: (terminal) => beforeDispose.current?.(terminal),
    onResize: (cols, rows) => resizeRef.current(cols, rows),
    onLink: (uri) => {
      if (!capability.hasLocal('forward.start')) {
        const remote = remoteOAuthInstructions(`run:${runID}`, uri)
        if (remote === null) return false
        toast.info(`Run ${remote.command}, then open this link on that machine.`, {
          description: uri,
          action: { label: 'Copy link', onClick: () => void copyText(uri, null) },
        })
        return true
      }
      return openOAuthLink(
        api,
        `run:${runID}`,
        uri,
        (port) => toast.success(`OAuth callback ready on localhost:${port}`),
        (err) => toast.error(`OAuth callback forward failed: ${errorSentence(err)}`),
      )
    },
  })
  const session = useRunTerminalSession({
    runID,
    run,
    terminal: controller.terminal,
    geometry: controller.geometry,
    setGeometry: controller.setGeometry,
    phone,
    identityKey,
    terminalCacheEpoch,
    automaticWrite,
    authorityKey,
    beginStructuralReplay: controller.beginStructuralReplay,
    cancelStructuralReplay: controller.cancelStructuralReplay,
    finishStructuralReplay: controller.finishStructuralReplay,
  })
  sendRef.current = session.send
  resizeRef.current = session.resize

  const acp = run.acp === true
  const streamed = acp && !run.switching
  const autoWrite = useRef(automaticWrite)
  autoWrite.current = automaticWrite
  useEffect(() => {
    if (!streamed) return
    const unsubscribe = subscribeSession(useStore, runID, autoWrite.current)
    useStore.getState().touchAcpSession(runID, sessionStreamOpen)
    return unsubscribe
  }, [streamed, runID])
  useEffect(() => {
    if (streamed && automaticWrite) allowSessionAutoWrite(runID)
  }, [streamed, automaticWrite, runID])
  const acpStream = useStore((s) => s.acpSessions[runID]?.stream)
  const acpControl = useStore((s) => s.acpSessions[runID]?.control)
  const acpTakeover = useStore((s) => s.acpSessions[runID]?.takeover)
  const acpTakeoverError = useStore((s) => s.acpSessions[runID]?.takeoverError)

  const { state, replaying } = session
  const live = acp ? acpStream === 'live' : state.connection === 'live'
  const controlMetadata = acp ? acpControl : session.controlMetadata
  const roomControl = live && steerable && !state.steerDenied ? controlMetadata : undefined
  const localControl = live && (acp || state.write) && controlMetadata?.has_control === true && steerable && !state.steerDenied
  const takeover = useTakeover({
    state: acp ? acpTakeover : session.takeover,
    error: acp ? acpTakeoverError : session.takeoverError,
    control: controlMetadata,
    enabled: live && steerable && !state.steerDenied,
    occupied: Boolean(roomStatus?.controller),
    request: acp ? (action, id, generation) => requestSessionTakeover(runID, action, id, generation) : session.requestTakeover,
  })
  const control = useMemo(() => acp ? {
    ...session,
    takeoverError: acpTakeoverError,
    takeControl: () => void requestSessionControl(runID, true),
    releaseControl: () => void requestSessionControl(runID, false),
  } : session, [acp, session, acpTakeoverError, runID])

  return {
    hasAgentTerminal,
    controller,
    session: control,
    takeover,
    starting,
    steerable,
    historyCache,
    beforeDispose,
    historyTools,
    readingHistory,
    setReadingHistory,
    roomControl,
    localControl,
    liveWritable: localControl && !replaying && !readingHistory,
    controlUnavailable: !live || state.steerDenied || !steerable,
  }
}

export type AgentTerminal = ReturnType<typeof useAgentTerminal>

const implicitControlWindowMs = 10_000

export function useImplicitControl(run: RunRecord, agent: AgentTerminal) {
  const self = useSelf()
  const holder = useStore((s) =>
    run.controller_member_id !== undefined ? run.controller_member_id || undefined : s.roomStatus[run.id]?.controller?.member_id)
  const implicit = !agent.localControl && run.member_id === self.id && holder === undefined && agent.steerable && !agent.controlUnavailable
  const queued = useRef<(() => void) | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => {
    if (!agent.localControl || !queued.current) return
    const act = queued.current
    queued.current = null
    clearTimeout(timer.current)
    act()
  }, [agent.localControl])
  useEffect(() => () => clearTimeout(timer.current), [])
  const withControl = (act: () => void) => {
    if (agent.localControl) return act()
    if (!implicit) return
    queued.current = act
    clearTimeout(timer.current)
    timer.current = setTimeout(() => { queued.current = null }, implicitControlWindowMs)
    agent.session.takeControl()
  }
  return { canAct: agent.localControl || implicit, withControl }
}
