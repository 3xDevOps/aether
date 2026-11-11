import { Fragment } from 'react'
import { toast } from 'sonner'
import { ChevronDown } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Menu, MenuCheckboxItem, MenuContent, MenuLabel, MenuTrigger } from '@/components/ui/menu'
import { StatusDot } from '@/components/ui/status-dot'
import { type Api, api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { ChildChoice, ChildChoiceSetting, ChildSettings } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { saveChildSettings } from '@/store/children'

function stateWords(choice: ChildChoice): string {
  switch (choice.status) {
    case 'not-signed-in':
      return 'Not signed in'
    case 'exhausted':
      return 'Usage used up'
    case 'ready':
      return choice.detail ? 'Signed out' : ''
  }
  return ''
}

function ChoiceLabel({ label, choice, on, faint = false }: { label: string; choice: ChildChoice; on: boolean; faint?: boolean }) {
  const state = stateWords(choice)
  return (
    <span className="flex min-w-0 flex-1 items-center gap-3" title={choice.detail}>
      <span className={cn('min-w-0 truncate', !on && !faint && 'text-muted')}>{label}</span>
      {state && <span className={cn('ml-auto shrink-0 text-ui-sm', faint ? 'text-icon-faint' : 'text-state-needs-you')}>{state}</span>}
    </span>
  )
}

const keepOpen = (event: Event) => event.preventDefault()

/**
 * The agents and accounts the member's subsessions may use, as a menu of
 * checkboxes. Every change saves at once; null on a server without choices.
 */
export function SubsessionAgents({ settings, id, disabled, client = api }: { settings: ChildSettings; id?: string; disabled?: boolean; client?: Api }) {
  const self = useStore((s) => s.info?.member.id)
  const off = settings.off ?? []
  const agentOff = (harness: string) => off.some((c) => c.harness === harness && !c.account_member_id)
  const accountOff = (harness: string, account: string) => off.some((c) => c.harness === harness && c.account_member_id === account)
  const groups = new Map<string, ChildChoice[]>()
  for (const choice of settings.choices ?? []) {
    if (choice.status !== 'not-installed') groups.set(choice.harness, [...(groups.get(choice.harness) ?? []), choice])
  }
  if (groups.size === 0) return null
  const offRows = [...groups].reduce((n, [harness, list]) => n + (agentOff(harness) ? 1 : list.filter((c) => accountOff(harness, c.account_member_id)).length), 0)
  const warnings = [...groups].flatMap(([harness, list]) => agentOff(harness) ? [] : list.filter((c) => !accountOff(harness, c.account_member_id) && stateWords(c)))

  // A refused change may still have been stored, so the menu then shows what the server holds.
  const turn = (choices: ChildChoiceSetting[]) => {
    choices.reduce((done, choice) => done.then(() => saveChildSettings({ choice }, client)), Promise.resolve(settings)).catch((err: unknown) => {
      toast.error(`Could not save subsession agents: ${errorSentence(err)}`)
      void saveChildSettings({}, client).catch(() => {})
    })
  }
  const accountLabel = (choice: ChildChoice) => choice.account_member_id === self ? 'Your account' : `${choice.account_name}'s account`
  const rowLabel = (choice: ChildChoice) => `${choice.display_name || choice.harness} · ${accountLabel(choice)}`

  return (
    <Menu>
      <MenuTrigger asChild>
        <Button id={id} type="button" variant="secondary" size="sm" disabled={disabled} title={warnings.map((c) => `${rowLabel(c)}: ${stateWords(c)}`).join('\n') || undefined}>
          {warnings.length > 0 && <StatusDot tone="needs-you" />}
          {offRows === 0 ? 'All on' : `${offRows} off`}
          {warnings.length > 0 && <span className="sr-only">, {warnings.length} {warnings.length === 1 ? 'needs' : 'need'} attention</span>}
          <ChevronDown />
        </Button>
      </MenuTrigger>
      <MenuContent align="end" className="w-80">
        <MenuLabel>
          Subsessions may use
          <span className="block font-normal">Applies to all your sessions</span>
        </MenuLabel>
        {[...groups].map(([harness, list]) => {
          const name = list[0].display_name || harness
          if (list.length === 1) {
            const [choice] = list
            const on = !agentOff(harness) && !accountOff(harness, choice.account_member_id)
            return (
              <MenuCheckboxItem
                key={harness}
                checked={on}
                onSelect={keepOpen}
                onCheckedChange={(next) => turn(next ? [{ harness, on: true }, { harness, account_member_id: choice.account_member_id, on: true }] : [{ harness }])}
              >
                <ChoiceLabel label={choice.account_member_id === self ? name : rowLabel(choice)} choice={choice} on={on} />
              </MenuCheckboxItem>
            )
          }
          return (
            <Fragment key={harness}>
              <MenuCheckboxItem checked={!agentOff(harness)} onSelect={keepOpen} onCheckedChange={(next) => turn([{ harness, on: next }])}>
                <span className={cn('min-w-0 truncate', agentOff(harness) && 'text-muted')}>{name}</span>
              </MenuCheckboxItem>
              {list.map((choice) => (
                <MenuCheckboxItem
                  key={choice.account_member_id}
                  className="pl-12 data-[disabled]:text-icon-faint data-[disabled]:opacity-100 [&>span:first-child]:left-7"
                  disabled={agentOff(harness)}
                  checked={!accountOff(harness, choice.account_member_id)}
                  onSelect={keepOpen}
                  onCheckedChange={(next) => turn([{ harness, account_member_id: choice.account_member_id, on: next }])}
                >
                  <ChoiceLabel label={accountLabel(choice)} choice={choice} on={!accountOff(harness, choice.account_member_id)} faint={agentOff(harness)} />
                </MenuCheckboxItem>
              ))}
            </Fragment>
          )
        })}
      </MenuContent>
    </Menu>
  )
}
