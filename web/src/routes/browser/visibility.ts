import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import { isTerminal, type RunRecord } from '@/store/runs'

const pollMs = 10_000

export function useBrowserTab(run: Pick<RunRecord, 'id' | 'status'>, supported: boolean): boolean {
  const [shown, setShown] = useState(false)
  const live = supported && !isTerminal(run.status)
  useEffect(() => {
    setShown(false)
    if (!live) return
    let alive = true
    const check = () => {
      if (document.hidden) return
      api.devBrowserStatus({ run_id: run.id })
        .then((status) => { if (alive) setShown(Boolean(status.session_id || status.running)) })
        .catch(() => { if (alive) setShown(false) })
    }
    check()
    const timer = setInterval(check, pollMs)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [run.id, live])
  return shown
}
