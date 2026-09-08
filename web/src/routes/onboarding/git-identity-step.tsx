// The onboarding step that settles who an agent's commits are authored as.
// The identity lives on the server, so every run this member starts carries
// it; this step only collects it, offering the machine's own git config as
// the default.

import { type ReactNode, useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import { actionRow } from '@/routes/onboarding/steps'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

/**
 * The Git identity step: the identity commits made in this member's runs
 * carry. What the member already saved wins; the machine's `git config`
 * fills whatever is still empty, and skipping leaves the server's fallback
 * in place.
 */
export function GitIdentityStep({
  client,
  caps,
  back,
  onNext,
}: {
  client: Api
  caps: Capability
  back?: ReactNode
  onNext: () => void
}) {
  const info = useStore((s) => s.info)
  const saved = info?.member
  const [name, setName] = useState(saved?.git_name ?? '')
  const [email, setEmail] = useState(saved?.git_email ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const probe = caps.hasLocal('git.identity')
  const [probing, setProbing] = useState(probe)
  // A field the user has typed in is theirs; the probe never overwrites it,
  // even when they emptied it on purpose.
  const typed = useRef({ name: false, email: false })

  useEffect(() => {
    if (!probe) return
    let cancelled = false
    client
      .localGitIdentity()
      .then((identity) => {
        if (cancelled) return
        if (!typed.current.name) setName((prev) => prev || identity.name)
        if (!typed.current.email) setEmail((prev) => prev || identity.email)
      })
      .catch((err) => {
        if (!cancelled) setError(message(err))
      })
      .finally(() => {
        if (!cancelled) setProbing(false)
      })
    return () => {
      cancelled = true
    }
  }, [client, probe])

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const member = await client.memberGit(name.trim(), email.trim())
      // Nothing on the wire announces a member's own change, so the store's
      // copy is what walking back into this step reads. Left stale, the
      // machine probe would overwrite what was just saved. The info read
      // here is the one at write time, not the render's: a disk-usage
      // refresh can land while the call is in flight.
      const s = useStore.getState()
      if (s.info) s.setInfo({ ...s.info, member })
      onNext()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const loading = useDelayed(probing)

  return (
    <section aria-label="Git identity" className="space-y-3">
      <h2 className="text-sm font-medium">Set your git identity</h2>
      <p className="text-sm text-muted-foreground">
        Every commit an agent makes in your runs is authored as this name and
        address, so the work you merge upstream credits you.
      </p>
      {loading && <Skeleton className="h-16 w-full" />}
      <form
        className="space-y-3"
        aria-label="Set git identity"
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <label className="block space-y-1 text-sm">
          Name
          <input
            className={field}
            value={name}
            placeholder="Ada Lovelace"
            disabled={busy}
            onChange={(e) => {
              typed.current.name = true
              setName(e.target.value)
            }}
          />
        </label>
        <label className="block space-y-1 text-sm">
          Email
          <input
            className={field}
            type="email"
            value={email}
            placeholder="ada@example.com"
            disabled={busy}
            onChange={(e) => {
              typed.current.email = true
              setEmail(e.target.value)
            }}
          />
        </label>
        {error && <p className="text-xs text-state-failed">{error}</p>}
        <div className={actionRow}>
          <Button
            type="submit"
            size="sm"
            disabled={busy || !name.trim() || !email.trim()}
          >
            Save
          </Button>
          <Button type="button" size="sm" variant="outline" onClick={onNext}>
            Skip
          </Button>
          {back}
        </div>
      </form>
    </section>
  )
}
