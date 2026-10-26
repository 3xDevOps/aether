import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useRef } from 'react'
import { api } from '@/lib/api'
import type * as apiModule from '@/lib/api'
import type { GatewayCapabilities } from '@/lib/types'
import { composerDraft, saveComposerDraft, useComposerDraft } from '@/routes/run/composer-state'
import { insertAt, TemplateInsert } from '@/routes/run/composer-templates'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, run, serverInfo, template, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const record = toRecord(run({ id: 'run_insert', status: 'running' }))

function mount(methods: GatewayCapabilities['methods'] = ['*'], busy = false) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    capabilities: { gateway: 'remote', methods, ws: [] },
    info: { ...serverInfo, member: alice },
    identityKey: crypto.randomUUID(),
    hydrated: true,
  })
  saveComposerDraft(record.id, { body: 'Look at this:', busy: busy ? 1 : undefined })
  const Box = () => {
    const textarea = useRef<HTMLTextAreaElement>(null)
    const { body } = useComposerDraft(record.id)
    return (
      <>
        <textarea aria-label="Message the agent" ref={textarea} value={body} onChange={(e) => saveComposerDraft(record.id, { body: e.target.value })} />
        <TemplateInsert run={record} textarea={textarea} disabled={busy} />
        <input aria-label="Notes" />
      </>
    )
  }
  return render(<Box />)
}

afterEach(() => saveComposerDraft(record.id, { body: '', busy: undefined }))

describe('insertAt', () => {
  it('replaces a selection and puts the caret after the inserted text', () => {
    expect(insertAt('fix the bug now', 'TASK', 4, 11)).toEqual({ body: 'fix TASK now', caret: 8 })
  })

  it('appends after a blank line when nothing is selected, or fills an empty draft', () => {
    expect(insertAt('first line\n', 'TASK', 3, 3)).toEqual({ body: 'first line\n\nTASK', caret: 16 })
    expect(insertAt('  ', 'TASK', 0, 0)).toEqual({ body: 'TASK', caret: 4 })
  })
})

describe('Insert a template', () => {
  it('fills the chosen template into the draft without sending it', async () => {
    vi.mocked(api.templateList).mockResolvedValue([{ ...template, task: 'Triage {{area}} issues.' }])
    mount()

    fireEvent.click(screen.getByRole('button', { name: 'Insert a template' }))
    const picker = within(await screen.findByRole('dialog', { name: 'Insert a template' }))
    await waitFor(() => expect(document.activeElement).toBe(picker.getByRole('searchbox', { name: 'Search templates' })))
    const insert = await picker.findByRole('button', { name: 'Insert' })
    expect(insert.getAttribute('aria-disabled')).toBe('true')
    const area = picker.getByRole('textbox', { name: 'area (required, no default)' })
    fireEvent.change(area, { target: { value: 'flaky' } })
    fireEvent.keyDown(area, { key: 'Enter' })

    await waitFor(() => expect(composerDraft(record.id).body).toBe('Look at this:\n\nTriage flaky issues.'))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('textbox', { name: 'Message the agent' }))
    expect(api.templateList).toHaveBeenCalledWith(workspace.id)
    expect(api.runInject).not.toHaveBeenCalled()
  })

  it('marks a scheduled template without a visit to the Templates page', async () => {
    vi.mocked(api.templateList).mockResolvedValue([template])
    mount()

    fireEvent.click(screen.getByRole('button', { name: 'Insert a template' }))
    const option = await screen.findByRole('option', { name: `Template ${template.name}` })
    await waitFor(() => expect(option.getAttribute('aria-describedby')).toBeTruthy())
    expect(api.scheduleList).toHaveBeenCalledWith(workspace.id)
  })

  it('leaves focus on a field clicked while the picker is open', async () => {
    vi.mocked(api.templateList).mockResolvedValue([template])
    mount()

    await userEvent.click(screen.getByRole('button', { name: 'Insert a template' }))
    await screen.findByRole('dialog', { name: 'Insert a template' })
    await userEvent.click(screen.getByRole('textbox', { name: 'Notes' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(document.activeElement).toBe(screen.getByRole('textbox', { name: 'Notes' }))
  })

  it('is disabled while the draft is sending', () => {
    mount(['*'], true)
    expect(screen.getByRole('button', { name: 'Insert a template' }).hasAttribute('disabled')).toBe(true)
  })

  it('is absent when the gateway cannot list templates', () => {
    mount(['run.get'])
    expect(screen.queryByRole('button', { name: 'Insert a template' })).toBeNull()
  })
})
