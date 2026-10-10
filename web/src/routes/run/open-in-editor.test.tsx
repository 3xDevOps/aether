import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { RunActions } from '@/components/run-actions'
import type { Run } from '@/lib/types'
import { editors, runHost, useOpenInEditor } from '@/routes/run/open-in-editor'
import { useStore } from '@/store'
import { alice, bob, run, runRecords, serverInfo, vera, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

function Menu() {
  const record = useStore((s) => s.runs.run_1!)
  const editor = useOpenInEditor(record)
  return (
    <>
      <RunActions run={record} extra={editor.item ? [editor.item] : []} />
      {editor.dialog}
    </>
  )
}

function open(over: Partial<Run> = {}, state: Partial<ReturnType<typeof useStore.getState>> = {}) {
  useStore.setState({
    runs: runRecords(run(over)),
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera },
    workspaces: { [workspace.id]: workspace },
    pausedRuns: {},
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    ...state,
  })
  return render(<Menu />)
}

async function more() {
  screen.getByRole('button', { name: 'More' }).focus()
  await userEvent.keyboard('{Enter}')
  return within(await screen.findByRole('menu'))
}

describe('Open in editor', () => {
  it('links each editor to the run host and shows the ssh and setup commands', async () => {
    open()
    await userEvent.click((await more()).getByRole('menuitem', { name: 'Open in editor…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Open in editor' }))
    for (const name of ['VS Code', 'Cursor', 'Zed']) expect(dialog.getByRole('button', { name })).toBeDefined()
    expect(dialog.getByText('ssh run_1.aether')).toBeDefined()
    expect(dialog.getByText('aether ssh-config')).toBeDefined()
    expect(editors.map((editor) => editor.link(runHost('run-gejd5cbk74')))).toEqual([
      'vscode://vscode-remote/ssh-remote+run-gejd5cbk74.aether/workspace',
      'cursor://vscode-remote/ssh-remote+run-gejd5cbk74.aether/workspace',
      'zed://ssh/run-gejd5cbk74.aether/workspace',
    ])
  })

  it('returns focus to More when the dialog closes', async () => {
    open()
    await userEvent.click((await more()).getByRole('menuitem', { name: 'Open in editor…' }))
    await screen.findByRole('dialog', { name: 'Open in editor' })
    await userEvent.keyboard('{Escape}')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'More' }))
  })

  it('says a paused run must be resumed first', async () => {
    open({}, { pausedRuns: { run_1: true } })
    const item = (await more()).getByRole('menuitem', { name: /Open in editor….*Resume the run first/ })
    expect(item.getAttribute('aria-disabled')).toBe('true')
  })

  it.each([
    ['a finished run', { status: 'completed' as const }, {}],
    ['a protected run of someone else', { protected: true }, { info: { ...serverInfo, member: bob } }],
    ['a viewer', {}, { info: { ...serverInfo, member: vera } }],
  ])('is not offered for %s', async (_name, over, state) => {
    open(over, state)
    expect((await more()).queryByRole('menuitem', { name: /Open in editor/ })).toBeNull()
  })

  it('is not offered on a phone', async () => {
    atViewport(390, { height: 844, pointer: 'coarse' })
    open()
    expect((await more()).queryByRole('menuitem', { name: /Open in editor/ })).toBeNull()
  })
})
