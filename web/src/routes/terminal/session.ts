import { useCallback, useEffect, useMemo, useRef, useState, type MutableRefObject } from 'react'
import type { Terminal } from '@xterm/xterm'
import { api } from '@/lib/api'
import type { Run, RunStatus } from '@/lib/types'
import {
  codeDenied,
  codeUnavailable,
  connectAttach,
  replayGate,
  type Attachment,
  type ControlMetadata,
  type ControlResult,
} from '@/routes/terminal/attach'
import { initialTerminal, type TerminalState } from '@/store/terminal'
import { useStore } from '@/store'

const endedStatuses: readonly RunStatus[] = [
  'completed',
  'merged',
  'abandoned',
  'failed',
  'interrupted',
]

const startingStatuses: readonly RunStatus[] = ['queued', 'provisioning']

export interface RunTerminalSessionInput {
  runID: string
  run?: Run
  active: boolean
  initialized: boolean
  terminal: Terminal | null
  geometry: () => { cols: number; rows: number }
  setGeometry: (cols: number, rows: number, reset?: boolean) => void | Promise<void>
  phone: boolean
  automaticWrite: boolean
  authorityKey: string
  onInvalidate?: () => void
  onWeight?: (weight: number) => void
  beginStructuralReplay?: () => number
  cancelStructuralReplay?: (generation: number) => void | Promise<void>
  finishStructuralReplay?: (generation: number) => void | Promise<void>
}

export interface RunTerminalSessionResult {
  state: TerminalState
  replaying: boolean
  controlMetadata: ControlMetadata | undefined
  sessionMissing: boolean
  send: (data: string) => void
  resize: (cols: number, rows: number) => void
  takeControl: (takeover?: boolean) => void
  releaseControl: () => void
  retry: () => void
}

interface SessionRefs {
  run?: Run
  active: boolean
  terminal: Terminal | null
  geometry: () => { cols: number; rows: number }
  setGeometry: (cols: number, rows: number, reset?: boolean) => void | Promise<void>
  phone: boolean
  automaticWrite: boolean
  authorityKey: string
  onInvalidate?: () => void
  onWeight?: (weight: number) => void
  beginStructuralReplay?: () => number
  cancelStructuralReplay?: (generation: number) => void | Promise<void>
  finishStructuralReplay?: (generation: number) => void | Promise<void>
}

function terminalWeight(terminal: Terminal | null): number {
  if (!terminal) return 0
  return (terminal.buffer.normal.length + terminal.buffer.alternate.length) * terminal.cols
}

function isEnded(status: RunStatus | undefined): boolean {
  return status !== undefined && endedStatuses.includes(status)
}

function useLatestRefs(input: RunTerminalSessionInput): MutableRefObject<SessionRefs> {
  const refs = useRef<SessionRefs>(input)
  refs.current = input
  return refs
}

export function useRunTerminalSession(input: RunTerminalSessionInput): RunTerminalSessionResult {
  const {
    runID,
    run,
    active,
    initialized,
    terminal,
    phone,
    automaticWrite,
    authorityKey,
  } = input
  const refs = useLatestRefs(input)
  const state = useStore((store) => store.terminals[runID] ?? initialTerminal)
  const setTerminal = useStore((store) => store.setTerminal)
  const markControlTaken = useStore((store) => store.markTerminalControlTaken)
  const [replaying, setReplaying] = useState(false)
  const [controlMetadata, setControlMetadata] = useState<ControlMetadata>()
  const [sessionMissing, setSessionMissing] = useState(false)
  const attachmentRef = useRef<Attachment | null>(null)
  const terminalRef = useRef<Terminal | null>(terminal)
  const activeRef = useRef(active)
  const latestActiveRef = useRef(active)
  const explicitWriteRef = useRef<boolean | null>(null)
  const structuralReplayRef = useRef<number | null>(null)
  const replayRevisionRef = useRef(0)
  const acceptedReplayRef = useRef(false)
  const abortedReplayRef = useRef(false)
  const sourceGeometryRef = useRef<Promise<void> | null>(null)
  const writeRevisionRef = useRef(0)
  const previousAuthorityRef = useRef(authorityKey)
  const previousAutomaticRef = useRef(automaticWrite)
  const previousPhoneRef = useRef(phone)
  const previousStatusRef = useRef(run?.status)
  const relaunchPendingRef = useRef(false)
  terminalRef.current = terminal
  latestActiveRef.current = active
  const gate = useMemo(
    () =>
      replayGate(
        (chunk, done) => {
          const current = terminalRef.current
          const revision = writeRevisionRef.current
          if (!current) {
            done?.()
            return
          }
          const write = () => {
            if (terminalRef.current !== current || writeRevisionRef.current !== revision) {
              done?.()
              return
            }
            current.write(chunk, () => {
              done?.()
              if (!latestActiveRef.current) refs.current.onWeight?.(terminalWeight(current))
            })
          }
          const sourceGeometry = sourceGeometryRef.current
          if (sourceGeometry) void sourceGeometry.then(write, write)
          else write()
        },
        (visible, full) => {
          const revision = ++replayRevisionRef.current
          if (visible) {
            if (full && structuralReplayRef.current === null) {
              structuralReplayRef.current = refs.current.beginStructuralReplay?.() ?? null
            }
            setReplaying(true)
            return
          }

          const generation = full ? structuralReplayRef.current : null
          if (full) structuralReplayRef.current = null
          const completion =
            generation === null
              ? undefined
              : refs.current.finishStructuralReplay?.(generation)
          void Promise.resolve(completion).then(
            () => {
              if (replayRevisionRef.current === revision) setReplaying(false)
            },
            () => {
              if (replayRevisionRef.current === revision) setReplaying(false)
            },
          )
        },
      ),
    [refs],
  )

  const updateControl = useCallback(
    (metadata: ControlMetadata) => {
      setControlMetadata(metadata)
      setTerminal(runID, { write: metadata.has_control })
      if (metadata.has_control && explicitWriteRef.current === true) markControlTaken()
    },
    [markControlTaken, runID, setTerminal],
  )

  const onControlResult = useCallback(
    (result: ControlResult) => {
      updateControl(result)
      if (result.ok) {
        setTerminal(runID, { message: null, refused: false })
        return
      }
      if (result.code === codeDenied) {
        explicitWriteRef.current = false
        setTerminal(runID, {
          write: false,
          steerDenied: true,
          message: result.error ?? 'control request refused',
        })
      } else {
        explicitWriteRef.current = false
        setTerminal(runID, {
          write: result.has_control,
          message: result.error ?? 'control request refused',
        })
      }
    },
    [runID, setTerminal, updateControl],
  )

  useEffect(() => {
    if (!run || !active || !initialized || !terminal || startingStatuses.includes(run.status)) return
    if (attachmentRef.current) return

    explicitWriteRef.current = null
    setControlMetadata(undefined)
    setSessionMissing(false)
    setTerminal(runID, { ...initialTerminal, write: false })
    gate.unmute()

    let attachment!: Attachment
    attachment = connectAttach(() => api.attachSocket(runID), {
      onData: gate.write,
      onAttached: (write, size, resumed) => {
        abortedReplayRef.current = false
        acceptedReplayRef.current = true
        if (!resumed) {
          writeRevisionRef.current++
          if (structuralReplayRef.current === null) {
            structuralReplayRef.current = refs.current.beginStructuralReplay?.() ?? null
          }
          const sourceGeometry = Promise.resolve(refs.current.setGeometry(size.cols, size.rows, true))
          sourceGeometryRef.current = sourceGeometry
          void sourceGeometry.then(
            () => {
              if (sourceGeometryRef.current === sourceGeometry) sourceGeometryRef.current = null
            },
            () => {
              if (sourceGeometryRef.current === sourceGeometry) sourceGeometryRef.current = null
            },
          )
        } else {
          sourceGeometryRef.current = null
          void refs.current.setGeometry(size.cols, size.rows, false)
        }
        updateControl({
          control_session_id: attachment.controlMetadata?.().control_session_id ?? '',
          control_generation: attachment.controlMetadata?.().control_generation ?? 0,
          has_control: write,
        })
        setSessionMissing(false)
        setTerminal(runID, { message: null, refused: false, write })
      },
      onReplayAbort: (full) => {
        abortedReplayRef.current = full
        acceptedReplayRef.current = false
        writeRevisionRef.current++
        sourceGeometryRef.current = null
        gate.cancel(full)
        if (!full) return
        const generation = structuralReplayRef.current
        structuralReplayRef.current = null
        if (generation !== null) {
          void Promise.resolve(refs.current.cancelStructuralReplay?.(generation)).catch(() => undefined)
        }
      },
      onReplayStart: (bytes, full) => {
        const accepted = acceptedReplayRef.current
        const aborted = abortedReplayRef.current
        acceptedReplayRef.current = false
        abortedReplayRef.current = false
        gate.start(full ? 'full' : 'delta')
        if (bytes === 0 && accepted) {
          void gate.write(new Uint8Array(), 'replay-end')
        } else if (bytes === 0 && full && aborted) {
          const writeRevision = writeRevisionRef.current
          const replayRevision = replayRevisionRef.current
          void Promise.resolve().then(() => {
            if (
              writeRevisionRef.current !== writeRevision ||
              replayRevisionRef.current !== replayRevision
            ) return
            void gate.write(new Uint8Array(), 'replay-end')
          })
        }
      },
      onControl: updateControl,
      onControlResult,
      onState: (connection) =>
        setTerminal(runID, connection === 'offline' ? { connection, write: false } : { connection }),
      onRefused: (message, code) => {
        abortedReplayRef.current = false
        acceptedReplayRef.current = false
        const writeRevision = ++writeRevisionRef.current
        const replayRevision = replayRevisionRef.current
        sourceGeometryRef.current = null
        const current = terminalRef.current
        const generation = structuralReplayRef.current
        structuralReplayRef.current = null
        const currentRefusal = () =>
          writeRevisionRef.current === writeRevision &&
          replayRevisionRef.current === replayRevision
        const reset = current
          ? refs.current.setGeometry(current.cols, current.rows, true)
          : undefined
        void Promise.resolve(reset)
          .catch(() => undefined)
          .then(() =>
            !currentRefusal() || generation === null
              ? undefined
              : refs.current.cancelStructuralReplay?.(generation),
          )
          .catch(() => undefined)
          .then(() => {
            if (!currentRefusal()) return
            gate.unmute()
            refs.current.onInvalidate?.()
          })
        setSessionMissing(code === codeUnavailable)
        setTerminal(runID, { message, refused: true, write: false })
      },
      onWriteDenied: () => {
        explicitWriteRef.current = false
        setTerminal(runID, { steerDenied: true, write: false })
      },
      onControlLost: () => {
        explicitWriteRef.current = false
        setControlMetadata(undefined)
        setTerminal(runID, { steerDenied: false, write: false })
      },
      onGeometry: refs.current.setGeometry,
      sessionPending: () => {
        const status = refs.current.run?.status
        return status !== undefined && !isEnded(status)
      },
      geometry: refs.current.geometry,
      wantsWrite: () => explicitWriteRef.current ?? refs.current.automaticWrite,
      follows: () => refs.current.phone,
      screen: () => true,
      interactive: () => true,
    })
    attachmentRef.current = attachment
    if (relaunchPendingRef.current) relaunchPendingRef.current = false
  }, [active, gate, initialized, run, runID, setTerminal, terminal, updateControl, onControlResult, refs])

  useEffect(() => {
    return () => {
      attachmentRef.current?.close()
      attachmentRef.current = null
      writeRevisionRef.current++
      sourceGeometryRef.current = null
      structuralReplayRef.current = null
    }
  }, [runID, terminal])

  useEffect(() => {
    const previous = activeRef.current
    if (previous && !active) {
      attachmentRef.current?.suspend()
    } else if (!previous && active) {
      const attachment = attachmentRef.current
      if (relaunchPendingRef.current && attachment) {
        relaunchPendingRef.current = false
        attachment.reopen()
      } else {
        attachment?.resume()
      }
    }
    activeRef.current = active
    if (!active && terminal) refs.current.onWeight?.(terminalWeight(terminal))
  }, [active, refs, terminal])

  useEffect(() => {
    const previous = previousStatusRef.current
    const current = run?.status
    previousStatusRef.current = current
    if (previous !== undefined && isEnded(previous) && current !== undefined && !isEnded(current)) {
      relaunchPendingRef.current = true
      if (active && attachmentRef.current?.isEnded()) {
        relaunchPendingRef.current = false
        previousPhoneRef.current = phone
        attachmentRef.current.reopen()
      }
    }
  }, [active, phone, run?.status])

  useEffect(() => {
    const previousAuthority = previousAuthorityRef.current
    previousAuthorityRef.current = authorityKey
    if (previousAuthority === authorityKey || !attachmentRef.current) return
    if (state.steerDenied) {
      attachmentRef.current.resetWriteDenial()
      explicitWriteRef.current = null
      setControlMetadata(undefined)
      setTerminal(runID, { steerDenied: false, write: false })
      attachmentRef.current.setControl(automaticWrite)
    }
  }, [authorityKey, automaticWrite, runID, setTerminal, state.steerDenied])

  useEffect(() => {
    const automaticChanged = previousAutomaticRef.current !== automaticWrite
    const phoneChanged = previousPhoneRef.current !== phone
    previousAutomaticRef.current = automaticWrite
    previousPhoneRef.current = phone
    const attachment = attachmentRef.current
    if (!attachment || !active || attachment.isEnded()) return
    if (automaticChanged && explicitWriteRef.current === null) {
      attachment.setControl(automaticWrite)
    }
    if (phoneChanged) attachment.reopen({ resume: true })
  }, [active, automaticWrite, phone])

  const send = useCallback((data: string) => {
    if (!latestActiveRef.current || !state.write || gate.muted()) return
    attachmentRef.current?.send(data)
  }, [gate, state.write])

  const resize = useCallback((cols: number, rows: number) => {
    attachmentRef.current?.resize(cols, rows)
  }, [])

  const takeControl = useCallback((takeover = false) => {
    explicitWriteRef.current = true
    attachmentRef.current?.setControl(true, takeover)
  }, [])

  const releaseControl = useCallback(() => {
    explicitWriteRef.current = false
    attachmentRef.current?.setControl(false)
  }, [])

  const retry = useCallback(() => {
    setSessionMissing(false)
    setTerminal(runID, { message: null, refused: false })
    const attachment = attachmentRef.current
    if (attachment && !attachment.isEnded()) {
      writeRevisionRef.current++
      replayRevisionRef.current++
      attachment.reopen()
    }
  }, [runID, setTerminal])

  return {
    state,
    replaying,
    controlMetadata,
    sessionMissing,
    send,
    resize,
    takeControl,
    releaseControl,
    retry,
  }
}

