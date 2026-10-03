import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from './command'

function expectSelected(label: string) {
  const option = screen.getByRole('option', { name: label })
  expect(option.getAttribute('aria-selected')).toBe('true')
  expect(option.getAttribute('aria-disabled')).toBe('false')
  expect(screen.getByRole('combobox').getAttribute('aria-activedescendant')).toBe(option.id)
  expect(screen.getByRole('listbox').getAttribute('aria-activedescendant')).toBe(option.id)
}

function names() {
  return screen.getAllByRole('option').map((option) => option.textContent)
}

test('initial selection is announced with input focus, and keyboard selection stays synchronized', async () => {
  render(
    <CommandDialog open>
      <CommandInput />
      <CommandList>
        <CommandGroup heading="Choices">
          <CommandItem disabled>Unavailable</CommandItem>
          <CommandItem>Alpha</CommandItem>
          <CommandItem>Beta</CommandItem>
          <CommandItem>Gamma</CommandItem>
        </CommandGroup>
      </CommandList>
    </CommandDialog>,
  )
  await waitFor(() => expectSelected('Alpha'))
  expect(document.activeElement).toBe(screen.getByRole('combobox'))
  await userEvent.keyboard('{ArrowDown}')
  expectSelected('Beta')
  await userEvent.keyboard('{End}')
  expectSelected('Gamma')
  await userEvent.keyboard('{Home}')
  expectSelected('Alpha')
  await userEvent.keyboard('{ArrowUp}')
  expectSelected('Alpha')
})

test('a supplied initial selection is announced rather than overwritten', async () => {
  render(
    <Command defaultValue="Gamma">
      <CommandInput />
      <CommandList>
        <CommandItem>Alpha</CommandItem>
        <CommandItem>Gamma</CommandItem>
      </CommandList>
    </Command>,
  )
  await waitFor(() => expectSelected('Gamma'))
})

test('ranked groups, ties and wrapped options restore their browse order on clear', async () => {
  const scores: Record<string, Record<string, number>> = {
    first: { Alpha: 0.4, Beta: 0.9, Gamma: 0.9, Delta: 0.2 },
    second: { Alpha: 0.8, Beta: 0.1, Gamma: 1, Delta: 0.2 },
    only: { Delta: 1 },
  }
  render(
    <Command filter={(value, query) => scores[query]?.[value] ?? 0}>
      <CommandInput />
      <CommandList>
        <CommandEmpty>No matches</CommandEmpty>
        <CommandGroup heading="First group">
          <div><CommandItem>Alpha</CommandItem></div>
          <div><CommandItem>Beta</CommandItem></div>
        </CommandGroup>
        <CommandGroup heading="Second group">
          <CommandItem>Gamma</CommandItem>
          <CommandItem>Delta</CommandItem>
        </CommandGroup>
      </CommandList>
    </Command>,
  )
  const input = screen.getByRole('combobox')
  expect(names()).toEqual(['Alpha', 'Beta', 'Gamma', 'Delta'])
  fireEvent.change(input, { target: { value: 'first' } })
  await waitFor(() => expect(names()).toEqual(['Beta', 'Alpha', 'Gamma', 'Delta']))
  expectSelected('Beta')
  fireEvent.change(input, { target: { value: 'second' } })
  await waitFor(() => expect(names()).toEqual(['Gamma', 'Delta', 'Alpha', 'Beta']))
  expectSelected('Gamma')
  fireEvent.change(input, { target: { value: 'only' } })
  await waitFor(() => expect(names()).toEqual(['Delta']))
  expectSelected('Delta')
  fireEvent.change(input, { target: { value: 'absent' } })
  await waitFor(() => expect(screen.queryAllByRole('option')).toEqual([]))
  expect(input.hasAttribute('aria-activedescendant')).toBe(false)
  fireEvent.change(input, { target: { value: '' } })
  await waitFor(() => expect(names()).toEqual(['Alpha', 'Beta', 'Gamma', 'Delta']))
  expectSelected('Alpha')
})

test('disabled, removed, renamed and late options never leave a stale active descendant', async () => {
  function Dynamic() {
    const [disabled, setDisabled] = useState(false)
    const [removed, setRemoved] = useState(false)
    const [renamed, setRenamed] = useState(false)
    const [added, setAdded] = useState(false)
    return (
      <>
        <button onClick={() => setDisabled(true)}>Disable alpha</button>
        <button onClick={() => setRemoved(true)}>Remove beta</button>
        <button onClick={() => setRenamed(true)}>Rename gamma</button>
        <button onClick={() => setAdded(true)}>Add delta</button>
        <Command>
          <CommandInput />
          <CommandList>
            <CommandItem disabled={disabled}>Alpha</CommandItem>
            {!removed && <CommandItem>Beta</CommandItem>}
            <CommandItem value={renamed ? 'Gamma renamed' : 'Gamma'}>{renamed ? 'Gamma renamed' : 'Gamma'}</CommandItem>
            {added && <CommandItem>Delta</CommandItem>}
          </CommandList>
        </Command>
      </>
    )
  }
  render(<Dynamic />)
  await waitFor(() => expectSelected('Alpha'))
  fireEvent.click(screen.getByText('Disable alpha'))
  await waitFor(() => expectSelected('Beta'))
  fireEvent.click(screen.getByText('Remove beta'))
  await waitFor(() => expectSelected('Gamma'))
  fireEvent.click(screen.getByText('Rename gamma'))
  await waitFor(() => expectSelected('Gamma renamed'))
  fireEvent.change(screen.getByRole('combobox'), { target: { value: 'Delta' } })
  expect(screen.queryAllByRole('option')).toEqual([])
  fireEvent.click(screen.getByText('Add delta'))
  await waitFor(() => expectSelected('Delta'))
  fireEvent.change(screen.getByRole('combobox'), { target: { value: '' } })
  await waitFor(() => expect(names()).toEqual(['Alpha', 'Gamma renamed', 'Delta']))
  expectSelected('Gamma renamed')
})

test('a caller can still override the dialog initial focus target', async () => {
  render(
    <CommandDialog open onOpenAutoFocus={(event) => {
      event.preventDefault()
      document.getElementById('alternate-focus')?.focus()
    }}>
      <button id="alternate-focus">Alternate focus</button>
      <CommandInput />
      <CommandList><CommandItem>Alpha</CommandItem></CommandList>
    </CommandDialog>,
  )
  await waitFor(() => expect(document.activeElement).toBe(screen.getByText('Alternate focus')))
})

test('authoritative browse order follows live values and data reordering without resetting the query', async () => {
  type Row = { id: string; value: string; keywords?: string[] }
  function Results({ rows }: { rows: Row[] }) {
    return (
      <Command>
        <CommandInput />
        <CommandList browseOrder={rows.map((row) => row.value)}>
          <CommandEmpty>No matches</CommandEmpty>
          <CommandGroup heading="Live results">
            {rows.map((row) => (
              <CommandItem key={row.id} value={row.value} keywords={row.keywords}>
                {row.value}
              </CommandItem>
            ))}
          </CommandGroup>
        </CommandList>
      </Command>
    )
  }
  const { rerender } = render(<Results rows={[
    { id: 'one', value: 'Alpha' },
    { id: 'two', value: 'Beta' },
    { id: 'three', value: 'Gamma' },
  ]} />)
  const input = screen.getByRole('combobox')
  fireEvent.change(input, { target: { value: 'needle' } })
  expect(screen.queryAllByRole('option')).toEqual([])
  rerender(<Results rows={[
    { id: 'three', value: 'Gamma renamed', keywords: ['needle'] },
    { id: 'new', value: 'Delta' },
    { id: 'one', value: 'Alpha' },
    { id: 'two', value: 'Beta' },
  ]} />)
  await waitFor(() => expectSelected('Gamma renamed'))
  expect((input as HTMLInputElement).value).toBe('needle')
  fireEvent.change(input, { target: { value: '' } })
  await waitFor(() => expect(names()).toEqual(['Gamma renamed', 'Delta', 'Alpha', 'Beta']))
  expectSelected('Gamma renamed')
  fireEvent.change(input, { target: { value: 'needle' } })
  rerender(<Results rows={[
    { id: 'three', value: 'Gamma renamed', keywords: ['other'] },
    { id: 'new', value: 'Delta' },
    { id: 'one', value: 'Alpha' },
    { id: 'two', value: 'Beta' },
  ]} />)
  await waitFor(() => expect(screen.queryAllByRole('option')).toEqual([]))
  expect(screen.getByText('No matches')).toBeDefined()
  expect(input.hasAttribute('aria-activedescendant')).toBe(false)
})
