import { render, screen } from '@testing-library/react'
import { AgentList } from '@/components/agents/agent-list'
import { agentInfo } from '@/test/fixtures'

const problem = '~/.claude/.credentials.json: Claude OAuth access token is missing or invalid'

describe('AgentList', () => {
  it('says a signed-out login is signed out, with the problem, and offers Log in instead of Run', () => {
    const onSetUp = vi.fn()
    const { container } = render(
      <AgentList agents={[agentInfo({ display_name: 'Claude Code', login_found: true, login_problem: problem, login_blocks_launch: true })]} onRun={vi.fn()} onSetUp={onSetUp} />,
    )
    const row = screen.getByRole('listitem')
    expect(row.textContent).toContain('Signed out')
    expect(row.textContent).not.toContain('Login found')
    expect(row.textContent).toContain(problem)
    expect(screen.queryByRole('button', { name: 'Run Claude Code' })).toBeNull()
    screen.getByRole('button', { name: 'Log in Claude Code' }).click()
    expect(onSetUp).toHaveBeenCalled()
    expect(container.querySelector('[data-tone="needs-you"]')).not.toBeNull()
  })

  it("names an agent's refusal as a refused sign-in, not a signed-out login", () => {
    render(<AgentList agents={[agentInfo({ display_name: 'Claude Code', login_found: true, login_problem: 'the agent refused: Authentication required' })]} onRun={vi.fn()} onSetUp={vi.fn()} />)
    const row = screen.getByRole('listitem')
    expect(row.textContent).toContain('Sign-in refused')
    expect(row.textContent).toContain('the agent refused: Authentication required')
    expect(screen.getByRole('button', { name: 'Log in Claude Code' })).toBeDefined()
  })

  it('offers Run on a working login', () => {
    render(<AgentList agents={[agentInfo({ display_name: 'Claude Code', login_found: true })]} onRun={vi.fn()} onSetUp={vi.fn()} />)
    expect(screen.getByRole('listitem').textContent).toContain('Installed · Login found')
    expect(screen.getByRole('button', { name: 'Run Claude Code' })).toBeDefined()
  })
})
