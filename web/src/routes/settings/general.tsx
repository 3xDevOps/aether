import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Kbd } from '@/components/ui/kbd'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { surfaces } from '@/lib/surfaces'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
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

export function GeneralSections() {
  const theme = useStore((s) => s.theme)
  const setTheme = useStore((s) => s.setTheme)
  const textSize = useStore((s) => s.textSize)
  const setTextSize = useStore((s) => s.setTextSize)
  const singleKeys = useStore((s) => s.singleKeyShortcuts)
  const setSingleKeys = useStore((s) => s.setSingleKeyShortcuts)
  const member = useStore((s) => s.info?.member !== undefined)
  const openProfile = useStore((s) => s.setProfileOpen)
  const navigate = useStore((s) => s.navigate)
  const onboarding = surfaces(useCapability()).some((surface) => surface.name === 'onboarding')
  return (
    <>
      <SettingsSection title="Appearance" scope="This browser">
        <SettingRow
          id="theme"
          label="Theme"
          labelFor="settings-theme"
          control={(
            <Select value={theme} onValueChange={(value) => setTheme(value as Theme)}>
              <SelectTrigger id="settings-theme" className="w-full sm:w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {themes.map(({ value, label }) => <SelectItem key={value} value={value}>{label}</SelectItem>)}
              </SelectContent>
            </Select>
          )}
        />
        <SettingRow
          id="text-size"
          label="Text size"
          labelFor="settings-text-size"
          help="Interface text. Terminals keep their own zoom."
          control={(
            <Select value={textSize} onValueChange={(value) => setTextSize(value as TextSize)}>
              <SelectTrigger id="settings-text-size" className="w-full sm:w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {textSizes.map(({ value, label }) => <SelectItem key={value} value={value}>{label}</SelectItem>)}
              </SelectContent>
            </Select>
          )}
        />
      </SettingsSection>
      <SettingsSection title="Keyboard" id="keyboard" scope="This browser">
        <SettingRow
          label="Single-key shortcuts"
          labelFor="settings-single-keys"
          help={<>Keys such as <Kbd>n</Kbd>, <Kbd>?</Kbd> and <Kbd>g</Kbd> <Kbd>b</Kbd>. Turn off if speech input or a stray key triggers them.</>}
          control={<Checkbox id="settings-single-keys" checked={singleKeys} onCheckedChange={(checked) => setSingleKeys(checked === true)} />}
        />
      </SettingsSection>
      {member && (
        <SettingsSection title="Profile" id="profile" scope="Your account">
          <SettingRow
            label="Name, colour and git identity"
            help="How other members see you, and the author of your sessions' commits."
            control={<Button size="sm" variant="secondary" onClick={() => openProfile(true)}>Open profile</Button>}
          />
        </SettingsSection>
      )}
      {onboarding && (
        <SettingsSection title="Setup guide" id="setup-guide">
          <SettingRow
            label="Setup guide"
            help="Link this computer, add a repository, set up an agent and start a first session. It resumes where you left it."
            control={<Button size="sm" variant="secondary" onClick={() => navigate('onboarding')}>Open guide</Button>}
          />
        </SettingsSection>
      )}
    </>
  )
}
