import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createElement, Fragment } from 'react'
import { CommandPalette } from '@/components/palette'
import { ThemeEffect } from '@/components/theme'
import { createRootStore, useStore } from '@/store'
import { alice, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  // Vitest hoists this factory before static imports; load its fixture after hoisting.
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

it('changes and persists appearance through explicit palette choices on a server gateway', async () => {
  const previousState = useStore.getState()
  const root = document.documentElement
  const previousClass = root.className
  const previousTheme = root.dataset.theme
  const previousScheme = root.style.colorScheme
  const originalMatchMedia = window.matchMedia
  const matchMedia = vi.spyOn(window, 'matchMedia').mockImplementation((query) =>
    query === '(prefers-color-scheme: dark)'
      ? { ...originalMatchMedia(query), matches: true }
      : originalMatchMedia(query),
  )
  useStore.setState({
    theme: 'light',
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    runs: {},
    pausedRuns: {},
    paletteOpen: false,
    paletteDialog: null,
    paletteRunID: null,
    route: { name: 'board', params: {} },
    hydrated: true,
    capabilities: { gateway: 'remote', methods: ['run.list'], ws: ['events', 'attach'] },
  })
  const view = render(createElement(Fragment, null,
    createElement(ThemeEffect),
    createElement(CommandPalette),
  ))
  onTestFinished(() => {
    view.unmount()
    matchMedia.mockRestore()
    useStore.setState(previousState)
    root.className = previousClass
    if (previousTheme === undefined) delete root.dataset.theme
    else root.dataset.theme = previousTheme
    root.style.colorScheme = previousScheme
  })

  expect(root.classList.contains('dark')).toBe(false)
  fireEvent.keyDown(window, { key: 'k', ctrlKey: true })
  fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'Use dark theme' } })
  fireEvent.click(await screen.findByRole('option', { name: 'Use dark theme' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(root.classList.contains('dark')).toBe(true)
  expect(root.style.colorScheme).toBe('dark')
  expect(createRootStore().getState().theme).toBe('dark')

  fireEvent.keyDown(window, { key: 'k', ctrlKey: true })
  fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'Use light theme' } })
  fireEvent.click(await screen.findByRole('option', { name: 'Use light theme' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(root.classList.contains('dark')).toBe(false)
  expect(root.dataset.theme).toBe('light')
  expect(createRootStore().getState().theme).toBe('light')

  fireEvent.keyDown(window, { key: 'k', ctrlKey: true })
  fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'Use system theme' } })
  fireEvent.click(await screen.findByRole('option', { name: 'Use system theme' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(root.classList.contains('dark')).toBe(true)
  expect(root.dataset.theme).toBe('dark')
  expect(createRootStore().getState().theme).toBe('system')
})
