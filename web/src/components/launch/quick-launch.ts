import { toast } from 'sonner'
import { automaticFallback, fallbackParams } from '@/components/launch/launch-options'
import { fallbackFromSaved } from '@/components/launch/saved-defaults'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import type { LaunchDefaultsResult, LaunchMode } from '@/lib/types'
import { useStore } from '@/store'
import { type LaunchDefaultsRead, rememberedChoice, useLaunchDefaultsRead } from '@/store/launch-defaults'

export interface LastLaunch {
  harness: string
  mode: LaunchMode
  model?: string
}

/** The agent, mode and model the next launch preselects: the saved choices, else this browser's newest. */
export function useLastLaunch(): { last: LastLaunch | null; saved: LaunchDefaultsRead } {
  const saved = useLaunchDefaultsRead()
  const local = useStore((s) => s.launchDefaults)
  const harness = saved.result
    ? saved.result.defaults.agent
    : Object.entries(local).sort(([, a], [, b]) => b.at - a.at)[0]?.[0]
  if (!harness) return { last: null, saved }
  const { mode, model } = rememberedChoice(harness, saved.result)
  // Only the panel knows which mode an agent supports, so a launch that saved none is not offered one-click.
  return { last: mode ? { harness, mode, ...(model ? { model } : {}) } : null, saved }
}

/** Whether the saved choices name `last` in a way a launch would refuse; the panel shows why. */
export function savedRefused(last: LastLaunch, saved: LaunchDefaultsResult | null): boolean {
  return (saved?.invalid ?? []).some((issue) => issue.field === 'agent' || issue.field === 'account_member_id' || issue.field === `agents.${last.harness}` || issue.field.startsWith(`agents.${last.harness}.`))
}

/** Starts a run on `last` with no task, on the saved account and fallback, as the launch panel would. */
export async function quickLaunch(last: LastLaunch, saved: LaunchDefaultsResult | null): Promise<void> {
  const { activeWorkspace, info, upsertRun, navigate } = useStore.getState()
  const own = info?.member.id ?? ''
  try {
    const wanted = saved?.defaults.account_member_id ?? ''
    const policy = saved?.defaults.fallback
    let account = ''
    let fallback = automaticFallback
    let accounts = [] as Awaited<ReturnType<typeof api.accountList>>['accounts']
    if (wanted || (policy && policy.policy !== 'automatic')) {
      accounts = (await api.accountList()).accounts
      if (wanted && wanted !== own && !accounts.some((member) => member.id === wanted)) throw new Error('your saved account is no longer shared with you; choose another in New run')
      account = wanted === own ? '' : wanted
      fallback = fallbackFromSaved(policy, accounts)
    }
    const run = await api.runLaunch({
      workspace_id: activeWorkspace,
      harness: last.harness,
      mode: last.mode,
      ...(last.model ? { model: last.model } : {}),
      ...(account ? { account_member_id: account } : {}),
      ...(accounts.length ? fallbackParams(fallback, accounts, account || own) : {}),
    })
    upsertRun(run)
    navigate('run', { runId: run.id })
    toast.success('Run launched')
  } catch (err) {
    toast.error(`Launch failed: ${message(err)}`)
  }
}
