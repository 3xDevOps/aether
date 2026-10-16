import { UpdateCenter } from '@/components/update-banner'
import { UpdateChecks } from '@/components/update-checks'
import type { Api } from '@/lib/api'

/** The update reads App mounts, and the dialog AppShell mounts. */
export function UpdateHost({ client }: { client: Api }) {
  return (
    <>
      <UpdateChecks client={client} />
      <UpdateCenter client={client} />
    </>
  )
}
