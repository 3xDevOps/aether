import { Checkbox } from '@/components/ui/checkbox'
import { Kbd } from '@/components/ui/kbd'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { OnboardingSection } from '@/routes/onboarding/settings-section'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { ServerSection } from '@/routes/settings/server'
import { ThisComputerSection } from '@/routes/settings/this-computer'
import { UsageSection } from '@/routes/settings/usage'
import { useStore } from '@/store'
import type { TextSize, Theme } from '@/store/ui'

const themes: { value: Theme; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

const textSizes: { value: TextSize; label: string }[] = [
  { value: 'default', label: 'Default' },
  { value: 'large', label: 'Large' },
  { value: 'larger', label: 'Larger' },
]

function AppearanceSection() {
  const theme = useStore((s) => s.theme)
  const setTheme = useStore((s) => s.setTheme)
  const textSize = useStore((s) => s.textSize)
  const setTextSize = useStore((s) => s.setTextSize)
  const singleKeys = useStore((s) => s.singleKeyShortcuts)
  const setSingleKeys = useStore((s) => s.setSingleKeyShortcuts)
  return (
    <SettingsSection title="Appearance">
      <SettingRow
        label="Theme"
        labelFor="settings-theme"
        control={(
          <Select value={theme} onValueChange={(value) => setTheme(value as Theme)}>
            <SelectTrigger id="settings-theme" className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {themes.map(({ value, label }) => <SelectItem key={value} value={value}>{label}</SelectItem>)}
            </SelectContent>
          </Select>
        )}
      />
      <SettingRow
        label="Text size"
        labelFor="settings-text-size"
        help="Interface text. Terminals keep their own zoom."
        control={(
          <Select value={textSize} onValueChange={(value) => setTextSize(value as TextSize)}>
            <SelectTrigger id="settings-text-size" className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {textSizes.map(({ value, label }) => <SelectItem key={value} value={value}>{label}</SelectItem>)}
            </SelectContent>
          </Select>
        )}
      />
      <SettingRow
        label="Single-key shortcuts"
        labelFor="settings-single-keys"
        help={<>Keys such as <Kbd>n</Kbd>, <Kbd>?</Kbd> and <Kbd>g</Kbd> <Kbd>b</Kbd>. Turn off if speech input or a stray key triggers them.</>}
        control={<Checkbox id="settings-single-keys" checked={singleKeys} onCheckedChange={(checked) => setSingleKeys(checked === true)} />}
      />
    </SettingsSection>
  )
}

export function SettingsRoute({ client = api }: RouteProps & { client?: Api }) {
  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Settings" />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-8 px-4 py-6 sm:px-6">
          <AppearanceSection />
          <ThisComputerSection client={client} />
          <ServerSection />
          <UsageSection client={client} />
          <OnboardingSection />
        </div>
      </div>
    </div>
  )
}

registerRoute('settings', SettingsRoute)
