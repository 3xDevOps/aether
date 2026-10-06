import { toast } from 'sonner'
import { ApiError, type Api } from '@/lib/api'
import { runClearDone, runReleaseFinished } from '@/lib/commands'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

beforeEach(() => vi.clearAllMocks())

const finished = (n: number) =>
  Array.from({ length: n }, (_, i) => toRecord(run({ id: `run_${i}`, status: 'merged' })))

function deferredApi() {
  const calls: string[] = []
  const settle = new Map<string, { resolve: () => void; reject: (err: unknown) => void }>()
  const pending = (id: string) => {
    calls.push(id)
    return new Promise<never>((resolve, reject) => {
      settle.set(id, { resolve: () => resolve(undefined as never), reject })
    })
  }
  const api = { runArchive: vi.fn(pending), runRelease: vi.fn(pending) } as unknown as Api
  return { api, calls, settle }
}

describe('archive closed runs', () => {
  it('keeps six archives in flight and names the first failure in list order', async () => {
    const runs = finished(8)
    const ids = runs.map((r) => r.id)
    const { api, calls, settle } = deferredApi()
    const done = runClearDone(runs, { api, removeRun: vi.fn() })

    await vi.waitFor(() => expect(calls).toEqual(ids.slice(0, 6)))
    settle.get(ids[3])!.reject(new ApiError(500, 'fourth failed', -32001))
    await vi.waitFor(() => expect(calls).toEqual(ids.slice(0, 7)))
    settle.get(ids[6])!.reject(new ApiError(500, 'seventh failed', -32001))
    await vi.waitFor(() => expect(calls).toEqual(ids))
    for (const id of ids) settle.get(id)!.resolve()
    await done

    expect(toast.error).toHaveBeenCalledWith('Archived 6, 2 failed: fourth failed')
  })

  it('treats a run that is already gone as archived and drops it locally', async () => {
    const [kept, gone] = finished(2)
    const removeRun = vi.fn()
    const api = {
      runArchive: vi.fn(async (id: string) => {
        if (id === gone.id) throw new ApiError(404, 'run not found', -32000)
        return run({ id })
      }),
    } as unknown as Api
    await runClearDone([kept, gone], { api, removeRun })

    expect(removeRun).toHaveBeenCalledWith(gone.id)
    expect(toast.success).toHaveBeenCalledWith('Archived 2 runs')
  })
})

describe('free retained containers', () => {
  it('keeps going past a refusal and reports the real error', async () => {
    const [ok, bad] = finished(2)
    const api = {
      runRelease: vi.fn(async (id: string) => {
        if (id === bad.id) throw new ApiError(409, 'evidence is still pending', -32001)
        return {}
      }),
    } as unknown as Api
    await runReleaseFinished([ok, bad], { api })

    expect(api.runRelease).toHaveBeenCalledTimes(2)
    expect(toast.error).toHaveBeenCalledWith('Freed 1, 1 failed: evidence is still pending')
  })
})
