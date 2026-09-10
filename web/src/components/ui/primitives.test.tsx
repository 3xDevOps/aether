import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useState } from 'react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { openSelect } from '@/test/select'

// A select's trigger takes its accessible name from its own contents, so a
// label around it names nothing and the control announces itself as whichever
// option is chosen.
test('a select is named by a Label that points at it', () => {
  render(
    <>
      <Label htmlFor="mode">Mode</Label>
      <Select defaultValue="tui">
        <SelectTrigger id="mode">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="tui">Interactive</SelectItem>
        </SelectContent>
      </Select>
    </>,
  )

  expect(screen.getByRole('combobox', { name: 'Mode' })).toBeDefined()
})

// A `<details>` kept its body in the page and merely hid it; a Collapsible
// mounts it on open. Anything that read the body while shut reads nothing now.
test('a collapsible withholds its body until it is opened', () => {
  render(
    <Collapsible>
      <CollapsibleTrigger>What git did</CollapsibleTrigger>
      <CollapsibleContent>Everything up-to-date</CollapsibleContent>
    </Collapsible>,
  )
  expect(screen.queryByText('Everything up-to-date')).toBeNull()

  fireEvent.click(screen.getByRole('button', { name: 'What git did' }))

  expect(screen.getByText('Everything up-to-date')).toBeDefined()
})

// What the empty report in `select.tsx` is for. It cost the launch dialog its
// agent and left Launch disabled for good.
test('a select keeps a value that lands with its options, inside a form', async () => {
  function Late() {
    const [agents, setAgents] = useState<string[]>([])
    const [harness, setHarness] = useState('')
    useEffect(() => {
      setAgents(['claude'])
      setHarness('claude')
    }, [])
    return (
      <form>
        <Select value={harness} onValueChange={setHarness}>
          <SelectTrigger aria-label="Agent">
            <SelectValue placeholder="Choose an agent" />
          </SelectTrigger>
          <SelectContent>
            {agents.map((agent) => (
              <SelectItem key={agent} value={agent}>
                {agent}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </form>
    )
  }
  render(<Late />)

  await waitFor(() =>
    expect(screen.getByLabelText('Agent').textContent).toBe('claude'),
  )
})

// A different branch in Radix from the keyboard one: it commits on pointer-up,
// and only once it has seen a mouse pointer rather than a touch.
test('a select commits a choice made with the mouse', async () => {
  const onValueChange = vi.fn()
  render(
    <Select value="tui" onValueChange={onValueChange}>
      <SelectTrigger aria-label="Mode">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="tui">Interactive</SelectItem>
        <SelectItem value="headless">Headless</SelectItem>
      </SelectContent>
    </Select>,
  )

  const trigger = screen.getByLabelText('Mode')
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: 'mouse' })
  // The release that opened the list is Radix's to swallow; the reader is
  // still holding nothing, and the list stays up for the second press.
  fireEvent.pointerUp(trigger, { button: 0, pointerType: 'mouse' })

  const headless = await screen.findByRole('option', { name: 'Headless' })
  fireEvent.pointerMove(headless, { pointerType: 'mouse' })
  fireEvent.pointerUp(headless, { button: 0, pointerType: 'mouse' })

  expect(onValueChange).toHaveBeenCalledWith('headless')
  expect(screen.queryByRole('listbox')).toBeNull()
})

// The three keys a reader steers a list with. Every other select test picks a
// known option outright, so none of this is asserted anywhere else.
test('a select list takes arrow keys, type-ahead and Escape', async () => {
  const onValueChange = vi.fn()
  render(
    <Select value="alpha" onValueChange={onValueChange}>
      <SelectTrigger aria-label="Letter">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="alpha">Alpha</SelectItem>
        <SelectItem value="bravo">Bravo</SelectItem>
        <SelectItem value="hotel">Hotel</SelectItem>
      </SelectContent>
    </Select>,
  )
  await openSelect(screen.getByLabelText('Letter'))

  await userEvent.keyboard('{ArrowDown}')
  expect(document.activeElement?.textContent).toBe('Bravo')

  await userEvent.keyboard('h')
  expect(document.activeElement?.textContent).toBe('Hotel')

  await userEvent.keyboard('{Escape}')

  expect(screen.queryByRole('listbox')).toBeNull()
  expect(onValueChange).not.toHaveBeenCalled()
})

