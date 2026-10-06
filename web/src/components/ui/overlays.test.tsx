import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DropdownMenu as MenuPrimitive } from 'radix-ui'
import { toast } from 'sonner'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import {
  Menu,
  MenuCheckboxItem,
  MenuContent,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  MenuTrigger,
} from '@/components/ui/menu'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Toaster } from '@/components/ui/toast'
import { atViewport } from '@/test/viewport'

function ItemsMenu({ count }: { count: number }) {
  return (
    <Menu>
      <MenuTrigger asChild>
        <Button>Actions</Button>
      </MenuTrigger>
      <MenuContent>
        <MenuLabel>Run</MenuLabel>
        {Array.from({ length: count }, (_, i) => (
          <MenuItem key={i}>Item {i + 1}</MenuItem>
        ))}
        <MenuSeparator />
        <MenuCheckboxItem checked>Show archived</MenuCheckboxItem>
      </MenuContent>
    </Menu>
  )
}

async function openMenu() {
  screen.getByRole('button', { name: 'Actions' }).focus()
  await userEvent.keyboard('{Enter}')
  return screen.findByRole('menu')
}

test('a menu opens from the keyboard and arrows walk its items', async () => {
  render(<ItemsMenu count={2} />)
  const menu = await openMenu()
  const items = within(menu).getAllByRole('menuitem')
  await waitFor(() => expect(document.activeElement).toBe(items[0]))
  await userEvent.keyboard('{ArrowDown}')
  expect(document.activeElement).toBe(items[1])
  expect(within(menu).getByRole('separator')).toBeDefined()
  expect(within(menu).getByRole('menuitemcheckbox', { name: 'Show archived' }).getAttribute('aria-checked')).toBe('true')
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('menu')).toBeNull()
})

test('a long menu on a phone opens as a bottom sheet', async () => {
  atViewport(390, { pointer: 'coarse' })
  render(<ItemsMenu count={6} />)
  const menu = await openMenu()
  expect(menu.hasAttribute('data-sheet')).toBe(true)
  expect(menu.className).toContain('animate-sheet-up')
})

function GroupedItems() {
  return (
    <MenuPrimitive.Group aria-label="Runs">
      {Array.from({ length: 7 }, (_, i) => (
        <MenuItem key={i}>Run {i + 1}</MenuItem>
      ))}
    </MenuPrimitive.Group>
  )
}

test('items inside a group or a caller\'s component count toward the sheet', async () => {
  atViewport(390, { pointer: 'coarse' })
  render(
    <Menu>
      <MenuTrigger asChild>
        <Button>Actions</Button>
      </MenuTrigger>
      <MenuContent>
        <GroupedItems />
      </MenuContent>
    </Menu>,
  )
  const menu = await openMenu()
  expect(menu.hasAttribute('data-sheet')).toBe(true)
})

test('a short menu on a phone stays a popup', async () => {
  atViewport(390, { pointer: 'coarse' })
  render(<ItemsMenu count={5} />)
  const menu = await openMenu()
  expect(menu.hasAttribute('data-sheet')).toBe(false)
})

test('a long menu on a wide screen stays a popup', async () => {
  atViewport(1280)
  render(<ItemsMenu count={10} />)
  const menu = await openMenu()
  expect(menu.hasAttribute('data-sheet')).toBe(false)
  expect(menu.className).toContain('animate-overlay-in')
  expect(menu.className).toContain('motion-reduce:animate-none')
})

function OpenDialog({ variant }: { variant?: 'center' | 'side' | 'bottom' }) {
  return (
    <Dialog open>
      <DialogContent variant={variant}>
        <DialogTitle>Rename run</DialogTitle>
        <DialogDescription>Give the run a name.</DialogDescription>
      </DialogContent>
    </Dialog>
  )
}

test('a dialog is named by its title and closes from its Close button', async () => {
  const onOpenChange = vi.fn()
  render(
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogTitle>Rename run</DialogTitle>
        <DialogDescription>Give the run a name.</DialogDescription>
      </DialogContent>
    </Dialog>,
  )
  const dialog = screen.getByRole('dialog', { name: 'Rename run' })
  fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }))
  expect(onOpenChange).toHaveBeenCalledWith(false)
})

test('a dialog that opens on its own shows no hint on the control it focuses', async () => {
  fireEvent.keyDown(document.body, { key: 'Tab' })
  await act(() => new Promise((resolve) => setTimeout(resolve)))
  render(
    <Dialog defaultOpen>
      <DialogContent>
        <DialogTitle>Takeover request</DialogTitle>
        <DialogDescription>Someone wants the terminal.</DialogDescription>
      </DialogContent>
    </Dialog>,
  )
  await waitFor(() => expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true))
  expect(screen.queryByRole('tooltip')).toBeNull()
})

test('one Escape closes a hint and the dialog under it', async () => {
  render(
    <Dialog defaultOpen>
      <DialogContent>
        <DialogTitle>Rename run</DialogTitle>
        <DialogDescription>Give the run a name.</DialogDescription>
        <input aria-label="Name" />
      </DialogContent>
    </Dialog>,
  )
  const dialog = screen.getByRole('dialog', { name: 'Rename run' })
  within(dialog).getByRole('textbox').focus()
  await userEvent.tab()
  expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Close' }))
  await screen.findByRole('tooltip')
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('tooltip')).toBeNull()
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('a centred dialog becomes a bottom sheet below md, and stills under reduced motion', () => {
  render(<OpenDialog />)
  const dialog = screen.getByRole('dialog')
  expect(dialog.getAttribute('data-variant')).toBe('center')
  for (const token of ['animate-overlay-in', 'max-md:bottom-[var(--keyboard-inset,0px)]', 'max-md:animate-sheet-up', 'motion-reduce:animate-none']) {
    expect(dialog.className).toContain(token)
  }
})

test.each([
  ['side', 'animate-sheet-right'],
  ['bottom', 'animate-sheet-up'],
] as const)('a %s dialog slides in from its edge', (variant, animation) => {
  render(<OpenDialog variant={variant} />)
  const dialog = screen.getByRole('dialog')
  expect(dialog.getAttribute('data-variant')).toBe(variant)
  expect(dialog.className).toContain(animation)
})

test('an alert dialog focuses Cancel and paints its action as danger', async () => {
  render(
    <AlertDialog open>
      <AlertDialogContent>
        <AlertDialogTitle>Kill run</AlertDialogTitle>
        <AlertDialogDescription>The agent stops at once.</AlertDialogDescription>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction>Kill run</AlertDialogAction>
      </AlertDialogContent>
    </AlertDialog>,
  )
  const dialog = screen.getByRole('alertdialog', { name: 'Kill run' })
  await waitFor(() => expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Cancel' })))
  expect(within(dialog).getByRole('button', { name: 'Kill run' }).className).toContain('bg-state-failed')
})

test('a popover opens on its trigger and closes on Escape', async () => {
  render(
    <Popover>
      <PopoverTrigger asChild>
        <Button>Details</Button>
      </PopoverTrigger>
      <PopoverContent aria-label="Run details">Branch main</PopoverContent>
    </Popover>,
  )
  await userEvent.click(screen.getByRole('button', { name: 'Details' }))
  const popover = await screen.findByRole('dialog', { name: 'Run details' })
  expect(popover.className).toContain('shadow-overlay')
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('a toast wears the floating surface', async () => {
  render(<Toaster />)
  act(() => {
    toast('Branch pulled')
  })
  const text = await screen.findByText('Branch pulled')
  const card = text.closest('[data-sonner-toast]')
  expect(card?.className).toContain('bg-raised')
  expect(card?.className).toContain('shadow-overlay')
})
