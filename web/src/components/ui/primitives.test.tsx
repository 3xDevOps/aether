import { readFile } from 'node:fs/promises'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef, useEffect, useState } from 'react'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { openSelect } from '@/test/select'
import { openingTags, sourceFiles } from '@/test/sources'

test.each([
  ['Input', <Input aria-label="Task" />],
  ['Textarea', <Textarea aria-label="Task" />],
  ['Checkbox', <Checkbox aria-label="Task" />],
  [
    'SelectTrigger',
    <Select>
      <SelectTrigger aria-label="Task">
        <SelectValue />
      </SelectTrigger>
    </Select>,
  ],
  [
    'CollapsibleTrigger',
    <Collapsible>
      <CollapsibleTrigger aria-label="Task" />
    </Collapsible>,
  ],
])('%s draws the shared focus outline', (_, element) => {
  render(element)

  const control = screen.getByLabelText('Task')
  expect(control.className).toContain('focus-visible:outline-2')
  expect(control.className).toContain('focus-visible:outline-ring')
})

test('Label names the control it wraps, with no id on either', () => {
  render(
    <Label>
      Task
      <Input />
    </Label>,
  )

  expect(screen.getByLabelText('Task').tagName).toBe('INPUT')
})

// A select is the exception to the wrapping idiom above. Its trigger is a
// button, and a button takes its accessible name from its own contents, so a
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

// Upstream's label is a flex row in a heavier weight, with modifiers that
// answer a disabled peer. The house one wraps its control instead, so it has
// no peer and cannot afford the row: an overwrite from the registry would
// reweight every caption and turn the `flex-1` ones into rows.
test('Label carries the house type scale and nothing else', () => {
  render(<Label>Task</Label>)

  expect(screen.getByText('Task').className).toBe('text-sm')
})

// Four call sites focus or select a field through a ref. Were forwarding to
// break, every one of them would become a silent no-op.
test('a ref reaches the field itself', () => {
  const ref = createRef<HTMLInputElement>()

  render(<Input ref={ref} />)

  expect(ref.current?.tagName).toBe('INPUT')
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

// The empty string belongs to the placeholder, so an item carrying it is a row
// that can never be chosen and never shown. See the Styleguide in
// docs/dashboard-frontend.md; the three sentinels in the app exist for this.
test('no select item carries the value the placeholder reserves', async () => {
  const offenders: string[] = []
  for (const path of await sourceFiles()) {
    const tags = openingTags(await readFile(path, 'utf8'), ['SelectItem'])
    for (const tag of tags) {
      if (/\bvalue=(""|\{''\}|\{""\})/.test(tag.attributes)) offenders.push(path)
    }
  }

  expect(offenders).toEqual([])
})

test('no file outside components/ui draws a control the primitives own', async () => {
  const sources = await sourceFiles()
  expect(sources.length).toBeGreaterThan(50)

  const owned = ['input', 'textarea', 'label', 'select', 'details', 'summary']
  const offenders: string[] = []
  for (const path of sources) {
    if (path.includes('/components/ui/')) continue
    for (const tag of openingTags(await readFile(path, 'utf8'), owned)) {
      offenders.push(`${path}: <${tag.name}>`)
    }
  }

  expect(offenders).toEqual([])
})
