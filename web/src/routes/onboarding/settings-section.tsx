import { Button } from '@/components/ui/button'
import { surfaces } from '@/lib/surfaces'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

export function OnboardingSection() {
  const cap = useCapability()
  const navigate = useStore((s) => s.navigate)
  if (!surfaces(cap).some((surface) => surface.name === 'onboarding')) return null
  return (
    <SettingsSection title="Onboarding">
      <SettingRow
        label="Setup guide"
        help="Link this computer, add a repository, set up an agent and start a first run. It resumes where you left it."
        control={<Button size="sm" variant="secondary" onClick={() => navigate('onboarding')}>Open guide</Button>}
      />
    </SettingsSection>
  )
}
