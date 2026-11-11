import { Fragment } from 'react'
import { toast } from 'sonner'
import { ChevronDown } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Menu, MenuCheckboxItem, MenuContent, MenuLabel, MenuTrigger } from '@/components/ui/menu'
import { type Api, api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { ChildChoice, ChildChoiceSetting, ChildSettings } from '@/lib/types'
import { useStore } from '@/store'
import { saveChildSettings } from '@/store/children'

function isOff(off: ChildChoiceSetting[], harness: string, account: string): boolean {
  return off.some((c) => c.harness === harness && (!c.account_member_id || c.account_member_id === account))
}

function stateWords(choice: ChildChoice): string {
  switch (choice.status) {
    case 'not-signed-in':
      return 'Not signed in'
    case 'exhausted':
      return 'Usage used up'
  }
  return choice.detail ? 'May be signed out' : ''
}

function ChoiceLabel({ label, choice }: { label: string; choice: ChildChoice }) {
  const state = stateWords(choice)
  return (
    <span className="flex min-w-0 flex-1 items-center gap-3" title={choice.detail}>
      <span className="min-w-0 truncate">{label}</span>
      {state && <span className="ml-auto shrink-0 text-ui-sm text-state-needs-you">{state}</span>}
    </span>
  )
}

/**
 * The agents and accounts the member's subsessions may use, as a menu of
 * checkboxes. Every change saves at once; null on a server without choices.
 */
export function SubsessionAgents({ settings, id, disabled, client = api }: { settings: ChildSettings; id?: string; disabled?: boolean; client?: Api }) {
  const self = useStore((s) => s.info?.member.id)
  const off = settings.off ?? []
  const groups = new Map<string, ChildChoice[]>()
  for (const choice of settings.choices ?? []) {
    if (choice.status !== 'not-installed') groups.set(choice.harness, [...(groups.get(choice.harness) ?? []), choice])
  }
  if (groups.size === 0) return null
  const all = [...groups.values()].flat()
  const offCount = all.filter((c) => isOff(off, c.harness, c.account_member_id)).length

  // A refused change may still have been stored, so the menu then shows what the server holds.
  const turn = (choices: ChildChoiceSetting[]) => {
    choices.reduce((done, choice) => done.then(() => saveChildSettings({ choice }, client)), Promise.resolve(settings)).catch((err: unknown) => {
      toast.error(`Could not save subsession agents: ${errorSentence(err)}`)
      void saveChildSettings({}, client).catch(() => {})
    })
  }
  const accountLabel = (choice: ChildChoice) => choice.account_member_id === self ? 'Your account' : `${choice.account_name}'s account`

  return (
    <Menu>
      <MenuTrigger asChild>
        <Button id={id} type="button" variant="secondary" size="sm" disabled={disabled}>
          {offCount === 0 ? 'All agents' : `${all.length - offCount} of ${all.length} on`}
          <ChevronDown />
        </Button>
      </MenuTrigger>
      <MenuContent align="end" className="w-80">
        <MenuLabel>
          Subsessions may use
          <span className="block font-normal">Saved for all your sessions</span>
        </MenuLabel>
        {[...groups].map(([harness, list]) => {
          const name = list[0].display_name || harness
          const agentOff = off.some((c) => c.harness === harness && !c.account_member_id)
          if (list.length === 1) {
            const [choice] = list
            const accountOff = off.some((c) => c.harness === harness && c.account_member_id === choice.account_member_id)
            return (
              <MenuCheckboxItem
                key={harness}
                checked={!agentOff && !accountOff}
                onSelect={(event) => event.preventDefault()}
                onCheckedChange={(on) => turn(on ? [{ harness, on: true }, { harness, account_member_id: choice.account_member_id, on: true }] : [{ harness }])}
              >
                <ChoiceLabel label={choice.account_member_id === self ? name : `${name} · ${accountLabel(choice)}`} choice={choice} />
              </MenuCheckboxItem>
            )
          }
          return (
            <Fragment key={harness}>
              <MenuCheckboxItem checked={!agentOff} onSelect={(event) => event.preventDefault()} onCheckedChange={(on) => turn([{ harness, on: on === true }])}>
                {name}
              </MenuCheckboxItem>
              {list.map((choice) => (
                <MenuCheckboxItem
                  key={choice.account_member_id}
                  className="pl-11"
                  disabled={agentOff}
                  checked={!isOff(off, harness, choice.account_member_id)}
                  onSelect={(event) => event.preventDefault()}
                  onCheckedChange={(on) => turn([{ harness, account_member_id: choice.account_member_id, on: on === true }])}
                >
                  <ChoiceLabel label={accountLabel(choice)} choice={choice} />
                </MenuCheckboxItem>
              ))}
            </Fragment>
          )
        })}
      </MenuContent>
    </Menu>
  )
}
