import { isRunLocal } from '@/routes/browser/address'
import { useStore } from '@/store'
import { capability, type Capability } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

/** A live run on a gateway that serves the browser has a Browser view. */
export function hasBrowser(run: Pick<RunRecord, 'status'>, cap: Capability): boolean {
  return cap.hasMethod('dev.browser.status') && !isTerminal(run.status)
}

/** Hands a link printed in a run's terminal to that run's Browser. False
 * when the link is not run-local or the run has no Browser, so the caller
 * opens it the ordinary way. */
export function openRunLink(runID: string, uri: string): boolean {
  const state = useStore.getState()
  const run = state.runs[runID]
  if (!run || !isRunLocal(uri) || !hasBrowser(run, capability(state.capabilities))) return false
  state.requestBrowser(runID, uri)
  return true
}
