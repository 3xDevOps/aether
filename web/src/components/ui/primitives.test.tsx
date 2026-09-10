import { readFile } from 'node:fs/promises'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useState } from 'react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Label } from '@/components/ui/label'
import { openingTags, sourceFiles, type Tag } from '@/test/sources'
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

// The blocked look belongs to `buttonVariants`, where each variant can stand
// its own hover down. A call site that writes one by hand cannot: it lands
// after the variant, so `hover:bg-transparent` beats `hover:bg-secondary/80`
// and a blocked button loses the fill it paints at rest. That is how the first
// attempt at this went wrong.
test('no file hand-rolls a blocked control by erasing its hover', async () => {
  const offenders: string[] = []
  for (const path of await sourceFiles()) {
    if (path.includes('/components/ui/')) continue
    if (/hover:bg-transparent|hover:text-inherit/.test(await readFile(path, 'utf8'))) {
      offenders.push(path)
    }
  }

  expect(offenders).toEqual([])
})

/** Components that take a `title` of their own rather than handing one to the
 * DOM: `ViewHeader` prints it as a heading, `CommandDialog` names the dialog. */
const titleProps = ['ViewHeader', 'CommandDialog']

/**
 * Every `title` the app still writes, each a line somebody chose. A `title`
 * anywhere else is a regression; see the Styleguide in
 * docs/dashboard-frontend.md for which ones may stay and why. Each entry is
 * a whole tag rather than a tag name; `tagSignature` says why.
 */
const pointerHints = [
  'components/copyable-command.tsx: <code ref={codeRef} title={command}>',
  "components/feed-entry.tsx: <span role=\"img\" aria-label={actor?.display_name ?? 'system'} title={actor?.display_name ?? 'system'}>",
  'components/feed-entry.tsx: <span title={event.type}>',
  'components/feed-entry.tsx: <time title={event.time}>',
  'components/run-header.tsx: <h1 title={label}>',
  'components/run-header.tsx: <span role="img" aria-label="Protected: only the owner or an admin can steer or kill this run" title="Protected: only the owner or an admin can steer or kill this run">',
  'components/run-header.tsx: <span title={subtitle}>',
  'components/run-list.tsx: <time title={run.stateChangedAt}>',
  "components/shell/sidebar.tsx: <span aria-hidden title={inboxError ?? 'Requests waiting on a decision'}>",
  "components/shell/sidebar.tsx: <span aria-label={`${count} ${count === 1 ? 'run needs' : 'runs need'} you`} role=\"img\" title=\"Runs waiting on a human\">",
  'components/shell/status-bar.tsx: <span aria-label="Disk usage" title={diskBreakdown(disk)}>',
  'components/shell/status-bar.tsx: <span role="status" title={notice}>',
  'components/shell/status-bar.tsx: <span role="status" title={unreachableLabel[unreachable]}>',
  'components/shell/status-bar.tsx: <span title={`${label} · protocol ${protocol}`}>',
  'components/shell/status-bar.tsx: <span title={info.member.display_name}>',
  'components/view-header.tsx: <h1 title={title}>',
  'components/view-header.tsx: <span title={subtitle}>',
  'routes/board/harness-glyph.tsx: <span title={`${harness} (${mode})`}>',
  'routes/board/member-avatar.tsx: <span role="img" aria-label={name} title={name}>',
  'routes/board/run-card.tsx: <span ref={branchRef} title={run.branch}>',
  'routes/board/run-card.tsx: <span role="img" aria-label="Protected: only the owner or an admin can steer or kill this run" title="Protected: only the owner or an admin can steer or kill this run">',
  'routes/board/run-card.tsx: <span title="Paused">',
  'routes/board/run-card.tsx: <span title={run.last_commit}>',
  'routes/board/run-card.tsx: <time title={timestamps(card)}>',
  'routes/diff/index.tsx: <code title={state.base}>',
  'routes/diff/patch-view.tsx: <span title={file.path}>',
  'routes/files/index.tsx: <p title={selection.path}>',
  'routes/files/index.tsx: <span title="Changed in this run" aria-label="Changed in this run">',
  'routes/run.tsx: <code title={run.last_commit}>',
  'routes/settings/index.tsx: <dd title={link.repo}>',
  "routes/team/budget.tsx: <span title={lines.join('\\n')} aria-label={`Budget ${money.format(totals.costUSD)}${totals.advisory ? '+' : ''}`}>",
  "routes/team/presence.tsx: <span title={`Online: ${names.join(', ')}`} aria-label={`${online.length} online`}>",
  "routes/team/presence.tsx: <span title={`Watching: ${names.join(', ')}`}>",
  'routes/workspace.tsx: <dd title={workspace.base_branch}>',
  'routes/workspaces/index.tsx: <dd title={workspace.base_branch}>',
  'routes/workspaces/index.tsx: <h3 title={workspace.name}>',
]

/** The end of one attribute's value, whether it is quoted or an expression.
 * A template literal nests its own braces, so this counts rather than stops
 * at the first `}`. */
function valueEnd(source: string, from: number): number {
  if (source[from] === '"') return source.indexOf('"', from + 1) + 1
  let depth = 0
  for (let at = from; at < source.length; at += 1) {
    if (source[at] === '{') depth += 1
    else if (source[at] === '}' && (depth -= 1) === 0) return at + 1
  }
  return source.length
}

/** The opening tag as the allowlist records it: the whole tag on one line,
 * less `className` and `style`. Those two are long and say nothing about
 * whether a hint sits on something a keyboard can reach, where `tabIndex`, a
 * `role`, an `onClick` or a spread of trigger props say exactly that. Without
 * a signature the entry would be a file and a tag name, and a hint could move
 * from an inert span onto a focusable one in the same file unnoticed. */
function tagSignature(tag: Tag): string {
  let rest = tag.attributes.slice(1 + tag.name.length)
  for (const drop of [/\bclassName=/, /\bstyle=/]) {
    for (let at = rest.search(drop); at !== -1; at = rest.search(drop)) {
      rest = rest.slice(0, at) + rest.slice(valueEnd(rest, rest.indexOf('=', at) + 1))
    }
  }
  rest = rest.replace(/\s+/g, ' ').replace(/\/$/, '').trim()
  return `<${tag.name}${rest && ` ${rest}`}>`
}

test('every title left in the app is one a pointer alone was meant to read', async () => {
  const found: string[] = []
  for (const path of await sourceFiles()) {
    if (path.includes('/components/ui/')) continue
    // One pattern rather than a list of tag names: a name left off such a
    // list is exactly how a `title` on a control gets through. Dotted names
    // are the ones that matter most here - `Tooltip.Trigger` is the focusable
    // control this whole rule exists to protect.
    for (const tag of openingTags(await readFile(path, 'utf8'), [
      '[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*',
    ])) {
      if (!/\btitle=/.test(tag.attributes)) continue
      if (titleProps.includes(tag.name)) continue
      found.push(`${path.split('/src/')[1]}: ${tagSignature(tag)}`)
    }
  }

  expect(found.sort()).toEqual(pointerHints)
})

/** The body of each `DialogContent` in one file. None of them nest, so the
 * next closing tag is always this one's. */
function dialogBodies(source: string): string[] {
  const bodies: string[] = []
  for (let from = 0; ; ) {
    const open = source.indexOf('<DialogContent', from)
    if (open === -1) return bodies
    const close = source.indexOf('</DialogContent>', open)
    if (close === -1) return bodies
    bodies.push(source.slice(open, close))
    from = close
  }
}

// A confirm is an `alertdialog`; see the Styleguide in
// docs/dashboard-frontend.md.
test('no destructive action hides in a plain dialog', async () => {
  const offenders: string[] = []
  for (const path of await sourceFiles()) {
    if (path.includes('/components/ui/')) continue
    for (const body of dialogBodies(await readFile(path, 'utf8'))) {
      // The literal and the expression form both; a bare `destructive` would
      // catch `text-destructive` on a paragraph inside the same dialog.
      if (/variant=(?:"destructive"|\{[^}]*\bdestructive\b)/.test(body)) {
        offenders.push(path)
      }
    }
  }

  expect(offenders).toEqual([])
})
