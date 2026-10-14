import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { toast, Toaster } from 'sonner'
import { ApiError } from '@/lib/api'
import type { MemberMerge, MergeMember, MergePlan, MergeProposal, MergeSettings } from '@/lib/types'
import { ProposeForm, SignInsSection } from '@/routes/members/sign-ins'
import { useStore } from '@/store'
import { alice, bob, fakeApi, serverInfo, workspace } from '@/test/fixtures'

const settings = (display_name: string, extra: Partial<MergeSettings> = {}): MergeSettings => ({
  display_name,
  color: '#e6194b',
  harness_definitions: [],
  profile_heads: [],
  account_shared_with: [],
  mirror_workspaces: [],
  home: false,
  ...extra,
})

const side = (id: string, display_name: string, role: MergeMember['role'], extra: Partial<MergeMember> = {}): MergeMember => ({
  id,
  display_name,
  role,
  identities: [],
  devices: [],
  runs: 0,
  schedules: 0,
  settings: settings(display_name),
  ...extra,
})

// Alice, an admin with an SSH key, enters a code minted by her GitHub sign-in, a collaborator.
const minted = side('mem_alice_gh', 'Alice on GitHub', 'collaborator', {
  identities: [{ provider: 'github', login: 'alice-gh' }],
  devices: [{ id: 'dev_1', label: 'alice-laptop', status: 'approved' }],
  runs: 2,
})
const proposer = side(alice.id, 'Alice', 'admin', { public_key_fingerprint: 'SHA256:alicekey', runs: 5 })
const plan: MergePlan = {
  minted,
  proposer,
  role: 'collaborator',
  role_with_keep_admin: 'admin',
  sign_ins: ['SSH key SHA256:alicekey', 'github account alice-gh'],
  choices: [
    { keep_settings_of: alice.id, absorbed_id: minted.id, discarded: settings('Alice on GitHub', { git_name: 'Alice G', account_shared_with: [bob.id] }) },
    { keep_settings_of: minted.id, absorbed_id: alice.id, discarded: settings('Alice', { home: true, image: 'aether/mem_alice:saved' }) },
  ],
  plan_digest: 'digest-1',
}

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice, [bob.id]: bob },
    mergedInto: {},
    mergeProposals: [],
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
  })
}

function enterCode() {
  fireEvent.click(screen.getByRole('button', { name: 'Merge with another member' }))
  const dialog = within(screen.getByRole('dialog', { name: 'Merge with another member' }))
  fireEvent.mouseDown(dialog.getByRole('tab', { name: 'I have a merge code' }))
  return dialog
}

const discarded = () => within(screen.getByRole('list', { name: 'Discarded settings' })).getAllByRole('listitem').map((li) => li.textContent)

describe('sign-ins', () => {
  it('lists the tailnet login, SSH key, GitHub accounts and devices', async () => {
    seed()
    const client = fakeApi({
      memberIdentityList: vi.fn(async () => ({
        tailnet_login: 'alice@example.github',
        public_key_fingerprint: 'SHA256:alicekey',
        identities: [{ provider: 'github', subject: '42', login: 'alice-gh', created_at: '2026-10-01T00:00:00Z' }],
        devices: [{ id: 'dev_1', member_id: alice.id, provider: 'github', account: 'alice-gh', label: 'alice-laptop', status: 'pending' as const, fingerprint: 'SHA256:dev', created_at: '2026-10-01T00:00:00Z' }],
      })),
    })
    render(<SignInsSection client={client} self={alice} />)

    const list = within(await screen.findByRole('list', { name: 'Your sign-ins' }))
    expect(list.getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Tailnet loginalice@example.github',
      'SSH keySHA256:alicekey',
      'GitHub accountalice-gh',
    ])
    const devices = within(screen.getByRole('list', { name: 'Your devices' }))
    expect(devices.getByText('alice-laptop')).toBeDefined()
    expect(devices.getByText('Pending')).toBeDefined()
    expect(client.memberIdentityList).toHaveBeenCalledWith(alice.id)
  })

  it('shows the join link and the next step for a linked GitHub account', async () => {
    seed()
    const client = fakeApi({
      memberIdentityLink: vi.fn(async () => ({
        invitation: { id: 'inv_1', provider: 'github', login: 'alice-gh', member_id: alice.id, created_by: alice.id, created_at: '2026-10-14T00:00:00Z', expires_at: '2026-10-21T00:00:00Z' },
        link: 'https://edge.example/join/srv/inv_1#secret',
        server_id: 'srv',
      })),
    })
    render(<SignInsSection client={client} self={alice} />)
    fireEvent.click(screen.getByRole('button', { name: 'Link a GitHub account' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Link a GitHub account' }))
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: ' alice-gh ' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Create link' }))

    expect(await dialog.findByText('https://edge.example/join/srv/inv_1#secret')).toBeDefined()
    expect(client.memberIdentityLink).toHaveBeenCalledWith({ login: 'alice-gh' })
    expect(dialog.getByText(/Open this link on a device signed in to GitHub as alice-gh/)).toBeDefined()
    expect(dialog.getByText(/That device then waits until you approve it/)).toBeDefined()
  })
})

describe('propose a merge', () => {
  const proposal: MergeProposal = { id: 'prop_1', minted_id: minted.id, proposer_id: alice.id, keep_settings_of: alice.id, plan_digest: 'digest-1', expires_at: '2026-10-14T00:10:00Z' }

  it('needs a settings choice and the discard acknowledged, then sends the plan digest', async () => {
    seed()
    const onProposed = vi.fn()
    const client = fakeApi({ memberMergePropose: vi.fn(async () => proposal) })
    render(<ProposeForm client={client} self={alice} code="ABCD-EFGH" plan={plan} onProposed={onProposed} onRefused={() => {}} />)

    const merge = screen.getByRole('button', { name: 'Propose merge' }) as HTMLButtonElement
    expect(within(screen.getByRole('radiogroup')).getAllByRole('radio').every((r) => r.getAttribute('aria-checked') === 'false')).toBe(true)
    expect(screen.queryByRole('checkbox', { name: 'Discard the listed settings' })).toBeNull()
    expect(merge.disabled).toBe(true)

    fireEvent.click(screen.getByRole('radio', { name: 'Alice (this sign-in)' }))
    expect(discarded()).toEqual(['Display name Alice on GitHub', 'Colour #e6194b', 'Git name Alice G', 'Agent account shared with Bob'])
    expect(merge.disabled).toBe(true)
    fireEvent.click(screen.getByRole('radio', { name: 'Alice on GitHub (the other sign-in)' }))
    expect(discarded()).toEqual(['Display name Alice', 'Colour #e6194b', 'Saved environment image aether/mem_alice:saved', 'Home on the server (agent logins, git signing key, files), archived on the server'])
    fireEvent.click(screen.getByRole('checkbox', { name: 'Discard the listed settings' }))
    expect(merge.disabled).toBe(false)
    fireEvent.click(merge)

    await waitFor(() => expect(onProposed).toHaveBeenCalledWith(proposal))
    expect(client.memberMergePropose).toHaveBeenCalledWith({ code: 'ABCD-EFGH', keep_settings_of: minted.id, plan_digest: 'digest-1', discard_ack: true })
  })

  it('states the resulting role and offers keeping admin to the admin side only', () => {
    seed()
    const { rerender } = render(<ProposeForm client={fakeApi()} self={alice} code="C" plan={plan} onProposed={() => {}} onRefused={() => {}} />)
    expect(screen.getByText(/The merged member will be/).textContent).toContain('will be Collaborator.')
    fireEvent.click(screen.getByRole('checkbox', { name: 'Keep the merged member an admin' }))
    expect(screen.getByText(/The merged member will be/).textContent).toContain('will be Admin.')

    const asCollaborator = { ...plan, minted: { ...minted, role: 'admin' as const }, proposer: { ...proposer, role: 'collaborator' as const } }
    rerender(<ProposeForm key="other" client={fakeApi()} self={alice} code="C" plan={asCollaborator} onProposed={() => {}} onRefused={() => {}} />)
    expect(screen.queryByRole('checkbox', { name: 'Keep the merged member an admin' })).toBeNull()
    expect(screen.getByText(/The merged member will be/).textContent).toContain('or Admin if the admin side keeps admin when it accepts')
  })

  it('shows a refused proposal verbatim and drops the plan, so only a new preview proposes', async () => {
    seed()
    const refusal = 'member.merge.propose: the members changed since the preview; preview again'
    const client = fakeApi({
      memberMergePreview: vi.fn(async () => plan),
      memberMergePropose: vi.fn(async () => Promise.reject(new ApiError(200, refusal, -32010))),
    })
    render(<SignInsSection client={client} self={alice} />)
    const dialog = enterCode()
    fireEvent.change(dialog.getByLabelText('Merge code'), { target: { value: 'ABCD-EFGH' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Preview' }))
    fireEvent.click(await dialog.findByRole('radio', { name: 'Alice (this sign-in)' }))
    fireEvent.click(dialog.getByRole('checkbox', { name: 'Discard the listed settings' }))
    fireEvent.click(dialog.getByRole('button', { name: 'Propose merge' }))

    expect((await dialog.findByRole('alert')).textContent).toBe(refusal)
    expect(dialog.queryByRole('button', { name: 'Propose merge' })).toBeNull()
    expect((dialog.getByRole('button', { name: 'Preview' }) as HTMLButtonElement).disabled).toBe(false)
    expect(client.memberMergePropose).toHaveBeenCalledTimes(1)
  })

  it('drops the previewed plan when the code is edited', async () => {
    seed()
    const client = fakeApi({ memberMergePreview: vi.fn(async () => plan) })
    render(<SignInsSection client={client} self={alice} />)
    const dialog = enterCode()
    fireEvent.change(dialog.getByLabelText('Merge code'), { target: { value: 'ABCD-EFGH' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Preview' }))
    expect(await dialog.findByRole('button', { name: 'Propose merge' })).toBeDefined()

    fireEvent.change(dialog.getByLabelText('Merge code'), { target: { value: 'WXYZ-EFGH' } })
    expect(dialog.queryByRole('button', { name: 'Propose merge' })).toBeNull()
  })

  it('shows a refused preview verbatim and the proposal once proposed', async () => {
    seed()
    const refusal = 'member.merge.preview: both members have an SSH public key (mem_a and mem_b) and a member has at most one; reach the server with one key and merge the other sign-in'
    const client = fakeApi({
      memberMergePreview: vi.fn().mockRejectedValueOnce(new ApiError(200, refusal, -32010)).mockResolvedValue(plan),
      memberMergePropose: vi.fn(async () => proposal),
    })
    render(<SignInsSection client={client} self={alice} />)
    const dialog = enterCode()
    fireEvent.change(dialog.getByLabelText('Merge code'), { target: { value: 'ABCD-EFGH' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Preview' }))
    expect((await dialog.findByRole('alert')).textContent).toBe(refusal)

    fireEvent.click(dialog.getByRole('button', { name: 'Preview' }))
    expect(await dialog.findByRole('region', { name: 'The other sign-in' })).toBeDefined()
    expect(dialog.queryByRole('alert')).toBeNull()
    fireEvent.click(dialog.getByRole('radio', { name: 'Alice (this sign-in)' }))
    fireEvent.click(dialog.getByRole('checkbox', { name: 'Discard the listed settings' }))
    fireEvent.click(dialog.getByRole('button', { name: 'Propose merge' }))
    expect((await dialog.findByRole('status')).textContent).toContain('Alice on GitHub must accept it on the other sign-in')
  })
})

describe('accept a merge', () => {
  // Bob's view: Bob minted the code and Alice, an admin, proposed keeping her settings and admin.
  const proposed: MergeProposal & { plan: MergePlan } = {
    id: 'prop_1',
    minted_id: bob.id,
    proposer_id: alice.id,
    keep_settings_of: alice.id,
    proposer_keep_admin: true,
    plan_digest: 'digest-2',
    expires_at: '2026-10-14T00:10:00Z',
    plan: {
      ...plan,
      minted: side(bob.id, 'Bob', 'collaborator', { public_key_fingerprint: undefined, tailnet_login: 'bob@tailnet' }),
      choices: [{ keep_settings_of: alice.id, absorbed_id: bob.id, discarded: settings('Bob', { git_name: 'Bob B' }) }],
      plan_digest: 'digest-2',
    },
  }
  const merge: MemberMerge = {
    id: 'merge_1', survivor_id: alice.id, absorbed_id: bob.id, confirmed_by_kind: 'tailnet', confirmed_by: 'bob@tailnet',
    survivor_role_before: 'admin', absorbed_role_before: 'collaborator', role_after: 'admin',
    discarded: settings('Bob', { git_name: 'Bob B' }), absorbed: side(bob.id, 'Bob', 'collaborator'),
    archive_path: '/var/lib/aether/merged/merge_1/home', created_at: '2026-10-14T00:05:00Z',
  }

  const asBob = (client: ReturnType<typeof fakeApi>) => {
    seed()
    useStore.setState({ info: { ...serverInfo, member: bob }, mergeProposals: [proposed] })
    render(<SignInsSection client={client} self={bob} />)
  }

  it('accepts the proposed plan by its digest once the discard is acknowledged', async () => {
    const client = fakeApi({ memberMergePending: vi.fn(async () => [proposed]), memberMergeAccept: vi.fn(async () => ({ merge, member: alice })) })
    asBob(client)

    const banner = within(await screen.findByRole('region', { name: 'Merge proposal from Alice' }))
    expect(banner.getByText(/Their sign-ins: SSH key SHA256:alicekey/)).toBeDefined()
    expect(discarded()).toEqual(['Display name Bob', 'Colour #e6194b', 'Git name Bob B'])
    expect(banner.getByText(/The merged member will be/).textContent).toBe('The merged member will be Admin.')
    expect(banner.queryByRole('checkbox', { name: 'Keep the merged member an admin' })).toBeNull()
    const accept = banner.getByRole('button', { name: 'Accept and merge' }) as HTMLButtonElement
    expect(accept.disabled).toBe(true)
    fireEvent.click(banner.getByRole('checkbox', { name: 'Discard the listed settings' }))
    fireEvent.click(accept)

    expect((await screen.findByRole('status')).textContent).toContain('was merged into')
    expect(client.memberMergeAccept).toHaveBeenCalledWith({ proposal_id: 'prop_1', plan_digest: 'digest-2' })
  })

  it('shows the merge record after accepting from Sign-ins', async () => {
    asBob(fakeApi({
      memberMergePending: vi.fn().mockResolvedValueOnce([proposed]).mockResolvedValue([]),
      memberMergeAccept: vi.fn(async () => ({ merge, member: alice })),
    }))
    const banner = within(await screen.findByRole('region', { name: 'Merge proposal from Alice' }))
    fireEvent.click(banner.getByRole('checkbox', { name: 'Discard the listed settings' }))
    await act(async () => fireEvent.click(banner.getByRole('button', { name: 'Accept and merge' })))

    const record = await screen.findByRole('status')
    expect(record.textContent).toContain('Bob (mem_bob) was merged into Alice (mem_alice)')
    expect(record.textContent).toContain('/var/lib/aether/merged/merge_1/home')
    expect(screen.queryByRole('region', { name: 'Merge proposal from Alice' })).toBeNull()
  })

  it('shows a refused accept verbatim, re-reads the proposal and asks for the tick again', async () => {
    const refusal = 'member.merge.accept: the members changed since the preview; preview again'
    const changed = { ...proposed, refusal: 'the members changed since the preview; preview again' }
    const client = fakeApi({
      memberMergePending: vi.fn().mockResolvedValueOnce([proposed]).mockResolvedValue([changed]),
      memberMergeAccept: vi.fn(async () => Promise.reject(new ApiError(200, refusal, -32010))),
    })
    asBob(client)
    const banner = within(await screen.findByRole('region', { name: 'Merge proposal from Alice' }))
    fireEvent.click(banner.getByRole('checkbox', { name: 'Discard the listed settings' }))
    fireEvent.click(banner.getByRole('button', { name: 'Accept and merge' }))

    await waitFor(() => expect(screen.getAllByRole('alert').map((a) => a.textContent)).toContain(refusal))
    await waitFor(() => expect(banner.getByRole('checkbox', { name: 'Discard the listed settings' }).getAttribute('aria-checked')).toBe('false'))
    expect(banner.getByText('the members changed since the preview; preview again')).toBeDefined()
    fireEvent.click(banner.getByRole('checkbox', { name: 'Discard the listed settings' }))
    expect((banner.getByRole('button', { name: 'Accept and merge' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('lists a proposal whose plan the server refused, and the viewer\'s own, each with Cancel', async () => {
    seed()
    const unplanned: MergeProposal = { id: 'prop_2', minted_id: alice.id, proposer_id: bob.id, keep_settings_of: bob.id, plan_digest: 'd', expires_at: '2026-10-14T00:10:00Z', refusal: 'member mem_bob is pending; approve first: aether member approve mem_bob' }
    const outgoing: MergeProposal = { id: 'prop_3', minted_id: minted.id, proposer_id: alice.id, keep_settings_of: alice.id, plan_digest: 'd', expires_at: '2026-10-14T00:10:00Z', plan }
    const client = fakeApi({ memberMergePending: vi.fn(async () => [unplanned, outgoing]), memberMergeCancel: vi.fn(async () => ({ cancelled: 1 })) })
    render(<SignInsSection client={client} self={alice} />)

    const refused = within(await screen.findByRole('region', { name: 'Merge proposal from Bob' }))
    expect(refused.getByRole('alert').textContent).toBe(unplanned.refusal)
    const mine = within(screen.getByRole('region', { name: 'Your merge proposal to Alice on GitHub' }))
    expect(mine.getByText(/waits for them to accept it/)).toBeDefined()

    fireEvent.click(refused.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Merge proposal from Bob' })).toBeNull())
    expect(client.memberMergeCancel).toHaveBeenCalledWith('prop_2')
  })

  it('ignores a sign-ins answer older than the latest read', async () => {
    seed()
    let older!: (value: { identities: never[]; devices: never[]; public_key_fingerprint: string }) => void
    const client = fakeApi({
      memberIdentityList: vi.fn()
        .mockImplementationOnce(() => new Promise((resolve) => { older = resolve }))
        .mockResolvedValue({ identities: [], devices: [], public_key_fingerprint: 'SHA256:new' }),
    })
    render(<SignInsSection client={client} self={alice} />)
    act(() => useStore.getState().setDevices([], null))
    expect(await screen.findByText('SHA256:new')).toBeDefined()
    await act(async () => older({ identities: [], devices: [], public_key_fingerprint: 'SHA256:old' }))
    expect(screen.queryByText('SHA256:old')).toBeNull()
  })

  it('shows a link refused after its dialog closed as a toast', async () => {
    seed()
    let refuse!: (err: Error) => void
    const client = fakeApi({ memberIdentityLink: vi.fn(() => new Promise<never>((_, reject) => { refuse = reject })) })
    render(<><Toaster /><SignInsSection client={client} self={alice} /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Link a GitHub account' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Link a GitHub account' }))
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'alice-gh' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Create link' }))
    fireEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    await act(async () => refuse(new Error('member.identity.link: the edge reports this server unclaimed')))

    expect(await screen.findByText('member.identity.link: the edge reports this server unclaimed')).toBeDefined()
    toast.dismiss()
  })

  it('re-reads the sign-ins when the devices are re-read', async () => {
    seed()
    const client = fakeApi()
    render(<SignInsSection client={client} self={alice} />)
    await waitFor(() => expect(client.memberIdentityList).toHaveBeenCalledTimes(1))
    act(() => useStore.getState().setDevices([], null))
    await waitFor(() => expect(client.memberIdentityList).toHaveBeenCalledTimes(2))
  })

  it('shows a failed proposals read beside the proposal it followed', async () => {
    seed()
    const client = fakeApi({
      memberMergePreview: vi.fn(async () => plan),
      memberMergePropose: vi.fn(async () => ({ id: 'prop_1', minted_id: minted.id, proposer_id: alice.id, keep_settings_of: alice.id, plan_digest: 'digest-1', expires_at: '2026-10-14T00:10:00Z' })),
      memberMergePending: vi.fn().mockResolvedValueOnce([]).mockRejectedValue(new Error('member.merge.pending: connection closed')),
    })
    render(<SignInsSection client={client} self={alice} />)
    const dialog = enterCode()
    fireEvent.change(dialog.getByLabelText('Merge code'), { target: { value: 'ABCD-EFGH' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Preview' }))
    fireEvent.click(await dialog.findByRole('radio', { name: 'Alice (this sign-in)' }))
    fireEvent.click(dialog.getByRole('checkbox', { name: 'Discard the listed settings' }))
    fireEvent.click(dialog.getByRole('button', { name: 'Propose merge' }))

    expect(await dialog.findByRole('status')).toBeDefined()
    expect((await dialog.findByRole('alert')).textContent).toBe('Reading the open merge proposals failed: member.merge.pending: connection closed')
  })

  it('re-reads the merge history for a new signed-in identity, dropping the old one', async () => {
    seed()
    useStore.setState({ identityKey: 'server\u0000mem_alice' })
    const first: MemberMerge = {
      id: 'merge_old', survivor_id: alice.id, absorbed_id: 'mem_gone', confirmed_by_kind: 'ssh', confirmed_by: 'SHA256:k',
      survivor_role_before: 'admin', absorbed_role_before: 'viewer', role_after: 'viewer',
      discarded: settings('Gone'), absorbed: side('mem_gone', 'Gone', 'viewer'), created_at: '2026-10-14T00:05:00Z',
    }
    let second!: (merges: MemberMerge[]) => void
    const client = fakeApi({ memberMergeList: vi.fn().mockResolvedValueOnce([first]).mockImplementation(() => new Promise((resolve) => { second = resolve })) })
    render(<SignInsSection client={client} self={alice} />)
    const history = within(screen.getByRole('region', { name: 'Merge history' }))
    expect(await history.findByText(/Gone \(mem_gone\) was merged into/)).toBeDefined()

    act(() => useStore.setState({ identityKey: 'server\u0000mem_bob' }))
    expect(history.queryByText(/Gone \(mem_gone\)/)).toBeNull()
    await act(async () => second([]))
    expect(history.getByText('No merges.')).toBeDefined()
  })

  it('re-reads the sign-ins when a merge maps another member', async () => {
    seed()
    const client = fakeApi()
    render(<SignInsSection client={client} self={alice} />)
    await waitFor(() => expect(client.memberIdentityList).toHaveBeenCalledTimes(1))
    act(() => useStore.getState().setMembers([alice], { [bob.id]: alice.id }))
    await waitFor(() => expect(client.memberIdentityList).toHaveBeenCalledTimes(2))
    act(() => useStore.getState().setMembers([alice], { [bob.id]: alice.id }))
    expect(client.memberIdentityList).toHaveBeenCalledTimes(2)
  })
})
