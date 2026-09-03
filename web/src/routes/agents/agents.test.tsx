import { act, fireEvent, render, screen } from '@testing-library/react'
import { api } from '@/lib/api'
import { lookupRoute } from '@/routes/registry'
import '@/routes/agents'
import { splitArgv } from '@/routes/agents/wizard'
import { useStore } from '@/store'

// vi.mock factories are hoisted above static imports, so the fixture module
// must be loaded inside the factory (same as terminal.test.tsx).
vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})


function mount() {
  const View = lookupRoute('agents')
  if (!View) throw new Error('agents route not registered')
  return render(<View params={{}} />)
}

async function flush() {
  await act(async () => {})
}

beforeEach(() => {
  useStore.setState({
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events', 'attach'] },
  })
})


describe('splitArgv', () => {
  it('splits on spaces and drops empty words', () => {
    expect(splitArgv('mycli -p {task}')).toEqual(['mycli', '-p', '{task}'])
    expect(splitArgv('  mycli  {task} ')).toEqual(['mycli', '{task}'])
  })
})

describe('agents view', () => {
  it('lists agents with shipped and member badges', async () => {
    const view = mount()
    await flush()

    expect(screen.getByText('claude')).toBeDefined()
    expect(screen.getByText('shipped')).toBeDefined()
    expect(screen.getByText('myagent')).toBeDefined()
    expect(screen.getByText('member')).toBeDefined()
    view.unmount()
  })

  it('shows the add button with regular gateway capabilities', async () => {
    const view = mount()
    await flush()

    expect(screen.getByText('Add agent')).toBeDefined()
    view.unmount()
  })

  it('prefills argv templates and renders setup instructions', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'mycli' },
    })

    expect(screen.getByDisplayValue('mycli {task}')).toBeDefined()
    expect(screen.getByDisplayValue('mycli -p {task}')).toBeDefined()

    fireEvent.click(screen.getByText('Continue'))
    await flush()

    expect(screen.getByText(/Open your environment terminal/)).toBeDefined()
    expect(screen.getByText('aether terminal')).toBeDefined()
    expect(
      screen.getByText('install mycli into ~/.local/bin'),
    ).toBeDefined()
    view.unmount()
  })

  it('shows the shipped harness install script', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'claude' },
    })
    fireEvent.click(screen.getByText('Continue'))
    await flush()

    expect(screen.getByText('curl -fsSL https://claude.ai/install.sh | bash')).toBeDefined()
    view.unmount()
  })

  it('registers a custom agent after the member finishes setup', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'mycli' },
    })
    fireEvent.click(screen.getByText('Continue'))
    await flush()

    fireEvent.click(screen.getByText("I've installed and logged in"))
    await flush()

    expect(vi.mocked(api.agentRegister)).toHaveBeenCalledWith({
      name: 'mycli',
      executable: 'mycli',
      tui_args: ['mycli', '{task}'],
      headless_args: ['mycli', '-p', '{task}'],
    })
    expect(screen.getByText('Agent registered')).toBeDefined()
    view.unmount()
  })

  it('omits argv inputs for a shipped name', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'claude' },
    })

    expect(screen.queryByText('TUI command')).toBeNull()
    view.unmount()
  })
})
