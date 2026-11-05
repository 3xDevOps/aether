import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { AgentPicker } from '@/components/launch/agent-picker'
import { agentInfo } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

const agents = [
  agentInfo({ name: 'claude', display_name: 'Claude Code', login_found: true }),
  agentInfo({ name: 'codex', display_name: 'Codex', login_found: true }),
]

function Picker() {
  const [value, setValue] = useState('claude')
  return <AgentPicker label="Agent" agents={agents} value={value} onChange={setValue} />
}

it('collapses the agent list to the choice on a phone until Change', () => {
  atViewport(412, { pointer: 'coarse' })
  render(<Picker />)
  const rows = () => screen.getAllByRole('radio', { name: /^(Claude Code|Codex)/ })

  expect(rows()).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: 'Change agent' }))
  fireEvent.click(screen.getByRole('radio', { name: /^Codex/ }))

  expect(rows()).toHaveLength(1)
  expect(screen.getByRole('radio', { name: /^Codex/ }).getAttribute('aria-checked')).toBe('true')
})
