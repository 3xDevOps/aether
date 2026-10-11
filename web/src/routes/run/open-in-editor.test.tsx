import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { RunActions } from '@/components/run-actions'
import type { Run } from '@/lib/types'
import { editors, runHost, useOpenInEditor } from '@/routes/run/open-in-editor'
import { useStore } from '@/store'
import { alice, bob, run, runRecords, serverInfo, vera, workspace } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

const runID = 'run-gejd5cbk74'

function Menu() {
  const record = useStore((s) => Object.values(s.runs)[0]!)
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
    runs: runRecords(run({ id: runID, ...over })),
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
    expect(dialog.getByText('ssh run-gejd5cbk74.aether')).toBeDefined()
    expect(dialog.getByText('aether ssh-config')).toBeDefined()
    expect(editors.map((editor) => editor.link(runHost(runID)!))).toEqual([
      'vscode://vscode-remote/ssh-remote+run-gejd5cbk74.aether/workspace',
      'cursor://vscode-remote/ssh-remote+run-gejd5cbk74.aether/workspace',
      'zed://ssh/run-gejd5cbk74.aether/workspace',
    ])
  })

  it('builds a host only from a run id that is a host label', () => {
    expect(runHost('01j9z3k8q0000000000000000a')).toBe('01j9z3k8q0000000000000000a.aether')
    for (const id of ['', '-oProxyCommand=id', 'run-', 'run_1', 'Run-1', 'a.b', 'a/b', 'a b', 'a;b', '$(id)', "a'b", 'a'.repeat(64)]) {
      expect(runHost(id), id).toBeNull()
    }
  })

  // The desktop app lets a link out of its window only when its own pattern
  // matches, and links in agent output reach the same pattern.
  it('builds links the desktop app lets out, and the desktop app lets out nothing else', () => {
    const main = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), '../../../../desktop/main.js'), 'utf8')
    const source = /const editorLink = \/(.+)\/$/m.exec(main)
    expect(source, 'the editorLink pattern in desktop/main.js').not.toBeNull()
    const editorLink = new RegExp(source![1]!)
    for (const editor of editors) expect(editorLink.test(editor.link(runHost(runID)!)), editor.name).toBe(true)
    for (const link of [
      'vscode://vscode-remote/ssh-remote+-oProxyCommand.aether/workspace',
      'vscode://vscode-remote/ssh-remote+run-.aether/workspace',
      'vscode://vscode-remote/ssh-remote+run-gejd5cbk74.aether/workspace/../etc',
      'vscode://vscode-remote/ssh-remote+run-gejd5cbk74.aether/workspace?windowId=_blank',
      'vscode://vscode-remote/ssh-remote+run-gejd5cbk74.aether.example.com/workspace',
      'vscode://vscode-remote/ssh-remote+example.com/workspace',
      'vscode://vscode-remote/ssh-remote+RUN-1.aether/workspace',
      'vscode://file/etc/passwd',
      'vscode://ms-vscode.some-extension/run-gejd5cbk74.aether/workspace',
      'cursor://anysphere.cursor-deeplink/prompt?text=hi',
      'zed://ssh/-oProxyCommand.aether/workspace',
      'zed://ssh/user@run-gejd5cbk74.aether/workspace',
      'zed://extension/some-extension',
      `x${editors[0]!.link(runHost(runID)!)}`,
      `${editors[0]!.link(runHost(runID)!)}\n`,
    ]) {
      expect(editorLink.test(link), link).toBe(false)
    }
  })

  it('says that connecting takes control of a swarm worker, and only there', async () => {
    const { unmount } = open()
    await userEvent.click((await more()).getByRole('menuitem', { name: 'Open in editor…' }))
    expect(within(await screen.findByRole('dialog', { name: 'Open in editor' })).queryByText(/takes control/)).toBeNull()
    unmount()

    open({ mission_id: 'mission_1', mission_role: 'worker' })
    await userEvent.click((await more()).getByRole('menuitem', { name: 'Open in editor…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Open in editor' }))
    expect(dialog.getByText('Connecting takes control of this swarm worker')).toBeDefined()
    expect(dialog.getByText(/stops the integrator from steering it until control is released on the swarm page/)).toBeDefined()
  })

  it('returns focus to More when the dialog closes', async () => {
    open()
    await userEvent.click((await more()).getByRole('menuitem', { name: 'Open in editor…' }))
    await screen.findByRole('dialog', { name: 'Open in editor' })
    await userEvent.keyboard('{Escape}')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'More' }))
  })

  it('says a paused run must be resumed first', async () => {
    open({}, { pausedRuns: { [runID]: true } })
    const item = (await more()).getByRole('menuitem', { name: /Open in editor….*Resume the run first/ })
    expect(item.getAttribute('aria-disabled')).toBe('true')
  })

  it.each([
    ['a finished run', { status: 'completed' as const }, {}],
    ['a protected run of someone else', { protected: true }, { info: { ...serverInfo, member: bob } }],
    ['a viewer', {}, { info: { ...serverInfo, member: vera } }],
    ['a run whose id is not a host label', { id: 'run_1' }, {}],
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
