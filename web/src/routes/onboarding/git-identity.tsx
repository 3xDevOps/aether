import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'

/** Saved values win; the machine's `git config` fills only what is still empty. */
export function GitIdentityForm({ client, caps }: { client: Api; caps: Capability }) {
  const saved = useStore((s) => s.info?.member)
  const [name, setName] = useState(saved?.git_name ?? '')
  const [email, setEmail] = useState(saved?.git_email ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false)
  const probe = caps.hasLocal('git.identity')
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
    return () => {
      cancelled = true
    }
  }, [client, probe])

  const save = async () => {
    setBusy(true)
    setError(null)
    setDone(false)
    try {
      const member = await client.memberGit(name.trim(), email.trim())
      // No event announces a member's own change; a stale store would let the
      // probe overwrite it. Read info now: a refresh can land mid-call.
      const s = useStore.getState()
      if (s.info) s.setInfo({ ...s.info, member })
      setDone(true)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      aria-label="Git identity"
      className="flex min-w-0 flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault()
        void save()
      }}
    >
      <p className="text-ui text-muted">Commits your agents make are authored as this name and email.</p>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <FormField label="Name">
          <Input
            value={name}
            placeholder="Ada Lovelace"
            disabled={busy}
            onChange={(e) => {
              typed.current.name = true
              setDone(false)
              setName(e.target.value)
            }}
          />
        </FormField>
        <FormField label="Email">
          <Input
            type="email"
            value={email}
            placeholder="ada@example.com"
            disabled={busy}
            onChange={(e) => {
              typed.current.email = true
              setDone(false)
              setEmail(e.target.value)
            }}
          />
        </FormField>
      </div>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="submit"
          size="sm"
          variant="secondary"
          disabled={busy || !caps.hasMethod('member.git') || !name.trim() || !email.trim()}
        >
          Save identity
        </Button>
        {done && <span role="status" className="text-ui-sm text-muted">Saved</span>}
      </div>
    </form>
  )
}
