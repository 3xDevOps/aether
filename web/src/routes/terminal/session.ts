import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type MutableRefObject,
} from 'react'
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
import { initialTerminal, type TerminalRunFence, type TerminalState } from '@/store/terminal'
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
  terminal: Terminal | null
  geometry: () => { cols: number; rows: number }
  setGeometry: (cols: number, rows: number, reset?: boolean) => void | Promise<void>
  phone: boolean
  automaticWrite: boolean
  identityKey: string | null
  terminalCacheEpoch: number
  authorityKey: string
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
  terminal: Terminal | null
  geometry: () => { cols: number; rows: number }
  setGeometry: (cols: number, rows: number, reset?: boolean) => void | Promise<void>
  phone: boolean
  automaticWrite: boolean
  identityKey: string | null
  terminalCacheEpoch: number
  authorityKey: string
  beginStructuralReplay?: () => number
  cancelStructuralReplay?: (generation: number) => void | Promise<void>
  finishStructuralReplay?: (generation: number) => void | Promise<void>
}

interface StructuralReplayOwner {
  generation: number
  cancel?: (generation: number) => void | Promise<void>
  finish?: (generation: number) => void | Promise<void>
}

interface PendingAuthorityControl {
  authorityKey: string
  automaticWrite: boolean
  resetWriteDenial: boolean
}

function isEnded(status: RunStatus | undefined): boolean {
  return status !== undefined && endedStatuses.includes(status)
}

type FenceInput = Pick<
  RunTerminalSessionInput,
  'run' | 'identityKey' | 'terminalCacheEpoch' | 'authorityKey'
>

function fenceMatches<T extends TerminalRunFence>(
  fence: T | undefined,
  input: FenceInput,
): fence is T {
  return (
    input.identityKey !== null &&
    fence?.identityKey === input.identityKey &&
    fence.terminalCacheEpoch === input.terminalCacheEpoch &&
    fence.authorityKey === input.authorityKey &&
    fence.runCreatedAt === input.run?.created_at
  )
}

function currentFence(input: FenceInput): TerminalRunFence | null {
  if (input.identityKey === null || !input.run) return null
  return {
    identityKey: input.identityKey,
    terminalCacheEpoch: input.terminalCacheEpoch,
    authorityKey: input.authorityKey,
    runCreatedAt: input.run.created_at,
  }
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
    terminal,
    phone,
    automaticWrite,
    identityKey,
    terminalCacheEpoch,
    authorityKey,
  } = input
  const refs = useLatestRefs(input)
  const state = useStore((store) => store.terminals[runID] ?? initialTerminal)
  const storedWriteIntent = useStore((store) => store.terminalWriteIntents[runID])
  const setTerminal = useStore((store) => store.setTerminal)
  const setWriteIntent = useStore((store) => store.setTerminalWriteIntent)
  const clearWriteIntent = useStore((store) => store.clearTerminalWriteIntent)
  const markControlTaken = useStore((store) => store.markTerminalControlTaken)
  const setControlSession = useStore((store) => store.setTerminalControlSession)
  const clearControlSession = useStore((store) => store.clearTerminalControlSession)
  const [replaying, setReplaying] = useState(false)
  const [controlMetadata, setControlMetadata] = useState<ControlMetadata>()
  const [sessionMissing, setSessionMissing] = useState(false)
  const attachmentRef = useRef<Attachment | null>(null)
  const terminalRef = useRef<Terminal | null>(terminal)
  const intentMatches = fenceMatches(storedWriteIntent, input)
  const explicitWriteRef = useRef<boolean | null>(
    intentMatches ? storedWriteIntent.write : null,
  )
  const structuralReplayRef = useRef<StructuralReplayOwner | null>(null)
  const replayRevisionRef = useRef(0)
  const acceptedReplayRef = useRef(false)
  const abortedReplayRef = useRef(false)
  const sourceGeometryRef = useRef<Promise<void> | null>(null)
  const writeRevisionRef = useRef(0)
  const previousAuthorityRef = useRef(authorityKey)
  const pendingAuthorityControlRef = useRef<PendingAuthorityControl | null>(null)
  const previousAutomaticRef = useRef(automaticWrite)
  const previousPhoneRef = useRef(phone)
  const previousStatusRef = useRef(run?.status)
  const relaunchPendingRef = useRef(false)
  terminalRef.current = terminal
  // Render the new authority as revoked immediately; the layout cleanup below
  // makes the stored lease match before the browser can paint.
  const authorityChanged = previousAuthorityRef.current !== authorityKey
  const committedState =
    authorityChanged && (state.write || state.steerDenied)
      ? { ...state, write: false, steerDenied: false }
      : state
  const committedControlMetadata = authorityChanged ? undefined : controlMetadata

  // A route session owns all displayed attach state. Reset it before paint so
  // stale status/control/refusal state cannot remain visible while xterm is
  // still waiting for its font and `terminal` is null.
  useLayoutEffect(() => {
    setControlMetadata(undefined)
    setSessionMissing(false)
    setReplaying(false)
    setTerminal(runID, initialTerminal)
  }, [runID, setTerminal])

  // The write preference survives a route change only while all
  // fences still identify the same authenticated run and authority context.
  useLayoutEffect(() => {
    if (intentMatches) return
    explicitWriteRef.current = null
    if (storedWriteIntent) clearWriteIntent(runID)
  }, [clearWriteIntent, intentMatches, runID, storedWriteIntent])

  const rememberWriteIntent = useCallback(
    (write: boolean) => {
      explicitWriteRef.current = write
      const fence = currentFence({ run, identityKey, terminalCacheEpoch, authorityKey })
      if (fence === null) {
        clearWriteIntent(runID)
        return
      }
      setWriteIntent(runID, { ...fence, write })
    },
    [
      authorityKey,
      clearWriteIntent,
      identityKey,
      run,
      runID,
      setWriteIntent,
      terminalCacheEpoch,
    ],
  )
  const beginStructuralReplay = useCallback((): StructuralReplayOwner | null => {
    const owner = refs.current
    const generation = owner.beginStructuralReplay?.()
    return generation === undefined
      ? null
      : {
          generation,
          cancel: owner.cancelStructuralReplay,
          finish: owner.finishStructuralReplay,
        }
  }, [refs])
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
            current.write(chunk, done)
          }
          const sourceGeometry = sourceGeometryRef.current
          if (sourceGeometry) void sourceGeometry.then(write, write)
          else write()
        },
        (visible, full) => {
          const revision = ++replayRevisionRef.current
          if (visible) {
            if (full && structuralReplayRef.current === null) {
              structuralReplayRef.current = beginStructuralReplay()
            }
            setReplaying(full === true)
            return
          }

          const replay = full ? structuralReplayRef.current : null
          if (full) structuralReplayRef.current = null
          const completion =
            replay === null
              ? undefined
              : replay.finish?.(replay.generation)
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
    [beginStructuralReplay, refs],
  )

  // Stamped with the fences current at each control change, because the
  // authority can change while the attachment stays mounted.
  const rememberControlSession = useCallback(
    (controlSessionID: string) => {
      const fence = currentFence(refs.current)
      if (fence === null) clearControlSession(runID)
      else setControlSession(runID, { ...fence, controlSessionID })
    },
    [clearControlSession, refs, runID, setControlSession],
  )

  const updateControl = useCallback(
    (metadata: ControlMetadata) => {
      setControlMetadata(metadata)
      setTerminal(runID, { write: metadata.has_control })
      if (metadata.has_control && explicitWriteRef.current === true) markControlTaken()
      rememberControlSession(metadata.control_session_id)
    },
    [markControlTaken, rememberControlSession, runID, setTerminal],
  )

  const onControlResult = useCallback(
    (result: ControlResult) => {
      updateControl(result)
      if (result.ok) {
        setTerminal(runID, { message: null, refused: false })
        return
      }
      explicitWriteRef.current = null
      clearWriteIntent(runID)
      if (result.code === codeDenied) {
        setTerminal(runID, {
          write: false,
          steerDenied: true,
          message: result.error ?? 'control request refused',
        })
      } else {
        setTerminal(runID, {
          write: result.has_control,
          message: result.error ?? 'control request refused',
        })
      }
    },
    [clearWriteIntent, runID, setTerminal, updateControl],
  )

  useEffect(() => {
    if (!run || !terminal || startingStatuses.includes(run.status)) return
    if (attachmentRef.current) return

    setControlMetadata(undefined)
    setSessionMissing(false)
    setTerminal(runID, { ...initialTerminal, write: false })
    gate.unmute()

    const stored = useStore.getState().terminalControlSessions[runID]
    const reclaim = fenceMatches(stored, refs.current) ? stored : undefined
    if (stored && !reclaim) clearControlSession(runID)

    let attachment!: Attachment
    attachment = connectAttach(() => api.attachSocket(runID), {
      onData: gate.write,
      onAttached: (write, size, resumed) => {
        abortedReplayRef.current = false
        acceptedReplayRef.current = true
        if (!resumed) {
          writeRevisionRef.current++
          if (structuralReplayRef.current === null) {
            structuralReplayRef.current = beginStructuralReplay()
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
        const attachedControl = attachment.controlMetadata?.()
        updateControl({
          control_session_id: attachedControl?.control_session_id ?? '',
          control_generation: attachedControl?.control_generation ?? 0,
          has_control: write,
          ...(attachedControl?.position === undefined ? {} : { position: attachedControl.position }),
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
        const replay = structuralReplayRef.current
        structuralReplayRef.current = null
        if (replay !== null) {
          void Promise.resolve(replay.cancel?.(replay.generation)).catch(() => undefined)
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
        const replay = structuralReplayRef.current
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
            !currentRefusal() || replay === null
              ? undefined
              : replay.cancel?.(replay.generation),
          )
          .catch(() => undefined)
          .then(() => {
            if (!currentRefusal()) return
            gate.unmute()
          })
        setSessionMissing(code === codeUnavailable)
        if (code === codeDenied) {
          explicitWriteRef.current = null
          clearWriteIntent(runID)
        }
        setTerminal(runID, { message, refused: true, write: false })
      },
      onWriteDenied: () => {
        // False, not null: null falls through to automatic write and the
        // next attach would ask again. A denial stays a mirror.
        explicitWriteRef.current = false
        clearWriteIntent(runID)
        setTerminal(runID, { steerDenied: true, write: false })
      },
      onControlLost: () => {
        // An occupied lease must reconnect as a mirror. Leaving the ref null
        // makes an owner ask for write again and conflict in a loop.
        explicitWriteRef.current = false
        clearWriteIntent(runID)
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
    }, reclaim?.controlSessionID)
    // Before any ack: the server may grant the lease to an attach this route
    // leaves before hearing the answer.
    const controlSessionID = attachment.controlMetadata?.().control_session_id
    if (controlSessionID) rememberControlSession(controlSessionID)
    attachmentRef.current = attachment
    if (relaunchPendingRef.current) relaunchPendingRef.current = false
  }, [
    beginStructuralReplay,
    clearControlSession,
    clearWriteIntent,
    gate,
    run,
    runID,
    setTerminal,
    terminal,
    updateControl,
    onControlResult,
    refs,
    rememberControlSession,
  ])

  useEffect(() => {
    return () => {
      attachmentRef.current?.close()
      setTerminal(runID, initialTerminal)
      attachmentRef.current = null
      writeRevisionRef.current++
      replayRevisionRef.current++
      sourceGeometryRef.current = null
      const replay = structuralReplayRef.current
      structuralReplayRef.current = null
      if (replay !== null) {
        void Promise.resolve(replay.cancel?.(replay.generation)).catch(() => undefined)
      }
    }
  }, [runID, setTerminal, terminal])


  useEffect(() => {
    const previous = previousStatusRef.current
    const current = run?.status
    previousStatusRef.current = current
    if (previous !== undefined && isEnded(previous) && current !== undefined && !isEnded(current)) {
      relaunchPendingRef.current = true
      if (attachmentRef.current?.isEnded()) {
        relaunchPendingRef.current = false
        previousPhoneRef.current = phone
        attachmentRef.current.reopen()
      }
    }
  }, [phone, run?.status])

  useLayoutEffect(() => {
    const previousAuthority = previousAuthorityRef.current
    previousAuthorityRef.current = authorityKey
    if (previousAuthority === authorityKey) return
    explicitWriteRef.current = null
    clearWriteIntent(runID)
    previousAutomaticRef.current = automaticWrite
    setControlMetadata(undefined)
    setTerminal(runID, { steerDenied: false, write: false })
    pendingAuthorityControlRef.current = {
      authorityKey,
      automaticWrite,
      resetWriteDenial: state.steerDenied,
    }
  }, [
    authorityKey,
    automaticWrite,
    clearWriteIntent,
    runID,
    setTerminal,
    state.steerDenied,
  ])

  useEffect(() => {
    const pending = pendingAuthorityControlRef.current
    if (!pending || pending.authorityKey !== authorityKey) return
    pendingAuthorityControlRef.current = null
    const attachment = attachmentRef.current
    if (!attachment) return
    if (pending.resetWriteDenial) attachment.resetWriteDenial()
    attachment.setControl(pending.automaticWrite)
  }, [authorityKey])

  useEffect(() => {
    const automaticChanged = previousAutomaticRef.current !== automaticWrite
    const phoneChanged = previousPhoneRef.current !== phone
    previousAutomaticRef.current = automaticWrite
    previousPhoneRef.current = phone
    const attachment = attachmentRef.current
    if (!attachment || attachment.isEnded()) return
    if (automaticChanged && explicitWriteRef.current === null) {
      attachment.setControl(automaticWrite)
    }
    if (phoneChanged) attachment.reopen({ resume: true })
  }, [automaticWrite, phone])

  const send = useCallback((data: string) => {
    if (authorityChanged || !state.write || gate.muted()) return
    attachmentRef.current?.send(data)
  }, [authorityChanged, gate, state.write])

  const resize = useCallback((cols: number, rows: number) => {
    attachmentRef.current?.resize(cols, rows)
  }, [])

  const takeControl = useCallback((takeover = false) => {
    rememberWriteIntent(true)
    attachmentRef.current?.setControl(true, takeover)
  }, [rememberWriteIntent])

  const releaseControl = useCallback(() => {
    rememberWriteIntent(false)
    attachmentRef.current?.setControl(false)
  }, [rememberWriteIntent])

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
    state: committedState,
    replaying,
    controlMetadata: committedControlMetadata,
    sessionMissing,
    send,
    resize,
    takeControl,
    releaseControl,
    retry,
  }
}

