import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { SessionUsage } from '@/lib/session-types'
import { ContextWindow, type ContextCompact } from '@/routes/run/context-window'
import { useStore } from '@/store'
import type { AcpSession } from '@/store/sessions'
import { item, resetItems } from '@/test/acp-stream'

function seedTurns(...usages: SessionUsage[]) {
  const turns = usages.map((usage, index) => ({ turn: index + 1, closed: true, items: [item('usage', index + 1, { usage })] }))
  useStore.setState({ acpSessions: { run_1: { turns } as unknown as AcpSession } })
}

function mount(usage: SessionUsage, props: { model?: string; compact?: ContextCompact } = {}) {
  return render(<ContextWindow runID="run_1" harness="claude" usage={usage} {...props} />)
}

const trigger = (name: RegExp | string) => screen.getByRole('button', { name })

beforeEach(() => {
  resetItems()
  useStore.setState({ acpSessions: {} })
})

describe('the context window trigger', () => {
  it('names the share and the tokens, and shows no meter until it opens', () => {
    mount({ used: 86_400, size: 200_000 })
    expect(trigger('Context window, 43% used, 86.4K of 200K tokens').textContent).toBe('43%')
    expect(screen.queryByRole('meter')).toBeNull()
  })

  it('opens a breakdown with one meter, the model and the free tokens', async () => {
    mount({ used: 86_000, size: 200_000 }, { model: 'Sonnet 4.5' })
    await userEvent.click(trigger(/^Context window/))
    const panel = await screen.findByRole('dialog', { name: 'Context window' })
    const meter = within(panel).getByRole('meter', { name: 'Context window used' })
    expect(meter.getAttribute('aria-valuetext')).toBe('86K of 200K tokens used, 43%')
    expect(meter.getAttribute('aria-valuenow')).toBe('86000')
    expect(within(panel).getByText('Sonnet 4.5')).toBeDefined()
    expect(within(panel).getByText('Used').nextSibling?.textContent).toBe('86K')
    expect(within(panel).getByText('Free').nextSibling?.textContent).toBe('114K')
    expect(within(panel).getByText('114K left')).toBeDefined()
    expect(within(panel).queryByText('Session cost')).toBeNull()
    expect(within(panel).queryByRole('button', { name: 'Compact' })).toBeNull()
  })

  it('shows the session cost only when the agent reported one', async () => {
    mount({ used: 86_000, size: 200_000, cost: 1.42, currency: 'USD' })
    await userEvent.click(trigger(/^Context window/))
    const row = (await screen.findByText('Session cost')).parentElement!
    expect(row.textContent).toContain('1.42')
  })

  it('turns to the needs-you tone from 90 percent', async () => {
    mount({ used: 190_000, size: 200_000 })
    await userEvent.click(trigger(/^Context window/))
    const panel = await screen.findByRole('dialog')
    expect(within(panel).getByText('95%').getAttribute('data-tone')).toBe('needs-you')
    expect(within(panel).getByText('10K left').className).toContain('text-state-needs-you')
  })

  it('says how far past the window it is, with no free row', async () => {
    mount({ used: 210_000, size: 200_000 })
    await userEvent.click(trigger(/^Context window/))
    expect(await screen.findByText('10K over the window')).toBeDefined()
    expect(screen.queryByText('Free')).toBeNull()
    expect(screen.getByRole('meter').getAttribute('aria-valuenow')).toBe('200000')
  })

  it('names the latest turn when an earlier report is loaded', async () => {
    seedTurns({ used: 50_000, size: 200_000 }, { used: 86_000, size: 200_000 })
    mount({ used: 86_000, size: 200_000 })
    await userEvent.click(trigger(/^Context window/))
    const panel = await screen.findByRole('dialog')
    expect(within(panel).getByText('Earlier context').nextSibling?.textContent).toBe('50K')
    expect(within(panel).getByText('Latest turn').nextSibling?.textContent).toBe('36K')
    expect(within(panel).queryByText('Used')).toBeNull()
  })

  it('swaps the headline to a category while the pointer rests on its row', async () => {
    seedTurns({ used: 50_000, size: 200_000 }, { used: 86_000, size: 200_000 })
    mount({ used: 86_000, size: 200_000 })
    await userEvent.click(trigger(/^Context window/))
    const panel = await screen.findByRole('dialog')
    expect(within(panel).getByText('of 200K tokens')).toBeDefined()
    const row = within(panel.querySelector('ul')!).getByText('Latest turn')
    await userEvent.hover(row)
    expect(await within(panel).findByText(/· 18% of the window/)).toBeDefined()
    expect(within(panel).getByRole('meter').textContent).toContain('Latest turn')
    await userEvent.unhover(row)
    await waitFor(() => expect(within(panel).queryByText(/of the window/)).toBeNull())
  })

  it('singles out a category when its row takes keyboard focus', async () => {
    seedTurns({ used: 50_000, size: 200_000 }, { used: 86_000, size: 200_000 })
    mount({ used: 86_000, size: 200_000 })
    await userEvent.click(trigger(/^Context window/))
    const panel = await screen.findByRole('dialog')
    expect(within(panel).queryByText(/of the window/)).toBeNull()
    await userEvent.tab()
    expect(await within(panel).findByText(/· 25% of the window/)).toBeDefined()
    expect(within(panel).getByRole('meter').textContent).toContain('Earlier context')
    await userEvent.tab()
    expect(await within(panel).findByText(/· 18% of the window/)).toBeDefined()
    expect(within(panel).getByRole('meter').textContent).toContain('Latest turn')
  })

  it('leaves the headline alone when a touch tap focuses a row', async () => {
    seedTurns({ used: 50_000, size: 200_000 }, { used: 86_000, size: 200_000 })
    mount({ used: 86_000, size: 200_000 })
    await userEvent.click(trigger(/^Context window/))
    const panel = await screen.findByRole('dialog')
    const row = within(panel.querySelector('ul')!).getByText('Latest turn').closest('li')!
    fireEvent.pointerDown(row, { pointerType: 'touch' })
    act(() => row.focus())
    expect(within(panel).queryByText(/of the window/)).toBeNull()
  })

  it('shows the headline when keyboard focus reaches a row after a touch tap', async () => {
    seedTurns({ used: 50_000, size: 200_000 }, { used: 86_000, size: 200_000 })
    mount({ used: 86_000, size: 200_000 })
    await userEvent.click(trigger(/^Context window/))
    const panel = await screen.findByRole('dialog')
    const row = within(panel.querySelector('ul')!).getByText('Latest turn').closest('li')!
    fireEvent.pointerDown(row, { pointerType: 'touch' })
    act(() => row.focus())
    act(() => row.blur())
    act(() => row.focus())
    expect(within(panel).getByRole('meter').textContent).toContain('Latest turn')
  })

  it('closes on Escape and hands focus back to the trigger', async () => {
    mount({ used: 86_000, size: 200_000 })
    await userEvent.click(trigger(/^Context window/))
    await screen.findByRole('dialog')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(document.activeElement).toBe(trigger(/^Context window/))
  })
})

describe('Compact', () => {
  it('closes the popover and hands the command to the composer', async () => {
    const onCompact = vi.fn()
    mount({ used: 86_000, size: 200_000 }, { compact: { onCompact } })
    await userEvent.click(trigger(/^Context window/))
    await userEvent.click(await screen.findByRole('button', { name: 'Compact' }))
    expect(onCompact).toHaveBeenCalledOnce()
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(document.activeElement).not.toBe(trigger(/^Context window/))
  })

  it('does nothing while the composer says why it cannot', async () => {
    const onCompact = vi.fn()
    mount({ used: 86_000, size: 200_000 }, { compact: { onCompact, blocked: 'Clear the message box first.' } })
    await userEvent.click(trigger(/^Context window/))
    const button = await screen.findByRole('button', { name: 'Compact' })
    expect(button.getAttribute('aria-disabled')).toBe('true')
    await userEvent.click(button)
    expect(onCompact).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeDefined()
  })
})
