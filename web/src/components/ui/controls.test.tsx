import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Radio, RadioGroup } from '@/components/ui/radio'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { hintOn } from '@/test/tooltip'

test('an icon-only button is named by its label and shows it as a tooltip', async () => {
  render(
    <Button variant="ghost" size="icon" label="Close panel">
      <X />
    </Button>,
  )
  const button = screen.getByRole('button', { name: 'Close panel' })
  expect(await hintOn(button)).toBe('Close panel')
})

test('a hint replaces the tooltip text but not the name', async () => {
  render(
    <Button size="icon" label="Collapse sidebar" hint="Collapse sidebar · Ctrl+B">
      <X />
    </Button>,
  )
  const button = screen.getByRole('button', { name: 'Collapse sidebar' })
  expect(await hintOn(button)).toBe('Collapse sidebar · Ctrl+B')
})

test('a text button has no tooltip unless it is given a hint', () => {
  render(<Button>Save</Button>)
  const button = screen.getByRole('button', { name: 'Save' })
  fireEvent.keyDown(document.body, { key: 'Tab' })
  button.focus()
  expect(screen.queryByRole('tooltip')).toBeNull()
  expect(button.getAttribute('aria-describedby')).toBeNull()
})

test.each([
  ['a click', () => fireEvent.click(document.body)],
  ['a shortcut', () => fireEvent.keyDown(document.body, { key: 'b', ctrlKey: true })],
])('focus that %s moves opens no tooltip', (_, move) => {
  render(
    <Button size="icon" label="Close">
      <X />
    </Button>,
  )
  const button = screen.getByRole('button', { name: 'Close' })
  fireEvent.keyDown(document.body, { key: 'Tab' })
  move()
  button.focus()
  expect(screen.queryByRole('tooltip')).toBeNull()
})

test('an aria-disabled button keeps its tab stop; a disabled one does not', async () => {
  render(
    <>
      <Button aria-disabled>
        Merge
      </Button>
      <Button disabled>Gone</Button>
    </>,
  )
  await userEvent.tab()
  expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Merge' }))
  await userEvent.tab()
  expect(document.activeElement).toBe(document.body)
})

test('buttons size to 28px, 24px small, and 44px on a coarse pointer', () => {
  render(
    <>
      <Button>Default</Button>
      <Button size="sm">Small</Button>
      <Button size="icon-sm" label="Tiny">
        <X />
      </Button>
    </>,
  )
  const md = screen.getByRole('button', { name: 'Default' }).className
  expect(md).toContain('h-7')
  expect(md).toContain('coarse:h-11')
  expect(screen.getByRole('button', { name: 'Small' }).className).toContain('h-6')
  const tiny = screen.getByRole('button', { name: 'Tiny' }).className
  expect(tiny).toContain('size-6')
  expect(tiny).toContain('coarse:size-11')
})

test('the variants paint the tokens the design system names', () => {
  render(
    <>
      <Button variant="primary">Primary</Button>
      <Button variant="secondary">Secondary</Button>
      <Button variant="danger">Danger</Button>
    </>,
  )
  expect(screen.getByRole('button', { name: 'Primary' }).className).toContain('bg-accent')
  const secondary = screen.getByRole('button', { name: 'Secondary' }).className
  expect(secondary).toContain('bg-raised')
  expect(secondary).toContain('hover:not-aria-disabled:bg-hover-chrome')
  const danger = screen.getByRole('button', { name: 'Danger' }).className
  expect(danger).toContain('bg-state-failed')
  expect(danger).toContain('text-on-failed')
})

function ThreeTabs({ look }: { look?: 'underline' | 'segmented' }) {
  return (
    <Tabs defaultValue="one">
      <TabsList look={look} aria-label="Views">
        <TabsTrigger value="one">One</TabsTrigger>
        <TabsTrigger value="two">Two</TabsTrigger>
        <TabsTrigger value="three">Three</TabsTrigger>
      </TabsList>
      <TabsContent value="one">First panel</TabsContent>
      <TabsContent value="two">Second panel</TabsContent>
      <TabsContent value="three">Third panel</TabsContent>
    </Tabs>
  )
}

test('tabs move focus with arrows and open a tab only on Enter or Space', async () => {
  render(<ThreeTabs />)
  const [one, two, three] = screen.getAllByRole('tab')

  await userEvent.tab()
  expect(document.activeElement).toBe(one)
  await userEvent.keyboard('{ArrowRight}')
  expect(document.activeElement).toBe(two)
  expect([one.tabIndex, two.tabIndex]).toEqual([-1, 0])
  expect(screen.getByRole('tabpanel').textContent).toBe('First panel')

  await userEvent.keyboard('{Enter}')
  expect(two.getAttribute('aria-selected')).toBe('true')
  expect(screen.getByRole('tabpanel').textContent).toBe('Second panel')

  await userEvent.keyboard('{ArrowRight} ')
  expect(three.getAttribute('aria-selected')).toBe('true')
})

test('segmented tabs carry their look for the trigger styles', () => {
  render(<ThreeTabs look="segmented" />)
  expect(screen.getByRole('tablist').getAttribute('data-look')).toBe('segmented')
})

test('a form field labels its control and describes it with help and error', () => {
  render(
    <FormField label="Branch" help="Created from main." error="Already taken.">
      <Input />
    </FormField>,
  )
  const input = screen.getByRole('textbox', { name: 'Branch' })
  expect(input.getAttribute('aria-invalid')).toBe('true')
  const described = (input.getAttribute('aria-describedby') ?? '').split(' ')
  expect(described.map((id) => document.getElementById(id)?.textContent)).toEqual([
    'Created from main.',
    'Already taken.',
  ])
})

test('a form field without help or error describes nothing', () => {
  render(
    <FormField label="Notes">
      <Textarea />
    </FormField>,
  )
  const textarea = screen.getByRole('textbox', { name: 'Notes' })
  expect(textarea.getAttribute('aria-describedby')).toBeNull()
  expect(textarea.getAttribute('aria-invalid')).toBeNull()
})

test('fields are 28px on canvas, and 44px on a coarse pointer', () => {
  render(<Input aria-label="Name" />)
  const input = screen.getByRole('textbox', { name: 'Name' }).className
  for (const token of ['h-7', 'coarse:h-11', 'bg-canvas', 'border-control', 'rounded-control']) {
    expect(input).toContain(token)
  }
})

test('a checkbox toggles with Space and grows its hit area on a coarse pointer', async () => {
  render(<Checkbox aria-label="Protect" />)
  const box = screen.getByRole('checkbox', { name: 'Protect' })
  expect(box.className).toContain('coarse:after:-inset-3.5')
  box.focus()
  await userEvent.keyboard(' ')
  expect(box.getAttribute('aria-checked')).toBe('true')
})

test('a radio group is one tab stop and arrows move the choice', async () => {
  render(
    <RadioGroup defaultValue="a" aria-label="Mode">
      <Radio value="a" aria-label="Standard" />
      <Radio value="b" aria-label="Enhanced" />
    </RadioGroup>,
  )
  const [standard, enhanced] = screen.getAllByRole('radio')
  await userEvent.tab()
  expect(document.activeElement).toBe(standard)
  // Radix checks the radio it moves to only while the arrow is still down.
  await userEvent.keyboard('{ArrowDown>}')
  await waitFor(() => expect(enhanced.getAttribute('aria-checked')).toBe('true'))
  await userEvent.keyboard('{/ArrowDown}')
  expect(document.activeElement).toBe(enhanced)
  await userEvent.tab()
  expect(document.activeElement).toBe(document.body)
})
