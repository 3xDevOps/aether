import { fireEvent, render, screen } from '@testing-library/react'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar, initials } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code, CodeBlock } from '@/components/ui/code'
import { EmptyState } from '@/components/ui/empty-state'
import { Kbd } from '@/components/ui/kbd'
import { ListRow } from '@/components/ui/list-row'
import { PaneHeader } from '@/components/ui/pane-header'
import { SectionLabel } from '@/components/ui/section-label'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { StateLine, StatusDot, type Tone, toneClasses } from '@/components/ui/status-dot'

const tones: Tone[] = ['working', 'needs-you', 'failed', 'done', 'paused', 'neutral']

test('every tone draws a shape of its own', () => {
  const { container } = render(
    <>
      {tones.map((tone) => (
        <StatusDot key={tone} tone={tone} />
      ))}
    </>,
  )
  const shapes = [...container.querySelectorAll('[data-slot="status-dot"]')].map((dot) => dot.innerHTML)
  expect(new Set(shapes).size).toBe(tones.length)
})

test('a dot is an image only when it is the one copy of the state', () => {
  render(
    <>
      <StatusDot tone="failed" label="Failed" />
      <StatusDot tone="done" />
    </>,
  )
  expect(screen.getByRole('img', { name: 'Failed' })).toBeDefined()
  expect(screen.getAllByRole('img')).toHaveLength(1)
})

test('only a pulsing dot animates, and the stylesheet stills it under reduced motion', () => {
  const { container } = render(
    <>
      <StatusDot tone="working" pulse />
      <StatusDot tone="working" />
    </>,
  )
  const [pulsing, still] = container.querySelectorAll('[data-slot="status-dot"]')
  expect(pulsing.getAttribute('class')).toContain('state-pulse')
  expect(still.getAttribute('class')).not.toContain('state-pulse')
})

test('a state line reads its reason in full text only when the run needs you', () => {
  render(
    <>
      <StateLine tone="needs-you" trailing="2m">
        Asks to run a command
      </StateLine>
      <StateLine tone="working">Editing files</StateLine>
    </>,
  )
  expect(screen.getByText('Asks to run a command').className).toContain('text-text')
  expect(screen.getByText('Editing files').className).toContain('text-muted')
  expect(screen.getByText('2m').className).toContain('tabular-nums')
})

test('callouts and badges share one tone palette', () => {
  render(
    <>
      <Callout tone="failed" title="Push refused" actions={<Button>Retry</Button>}>
        The remote rejected the branch.
      </Callout>
      <Badge tone="failed">Failed</Badge>
      <Badge>3 runs</Badge>
    </>,
  )
  const { text, soft } = toneClasses('failed')
  expect(screen.getByText('Push refused').className).toContain(text)
  expect(screen.getByText('The remote rejected the branch.').closest('[data-slot="callout"]')?.className).toContain(soft)
  expect(screen.getByRole('button', { name: 'Retry' })).toBeDefined()
  expect(screen.getByText('Failed').className).toContain(soft)
  expect(screen.getByText('3 runs').className).toContain(toneClasses('neutral').soft)
})

test('a pane header holds the view heading, its toolbar and the sidebar button', () => {
  const onOpenSidebar = vi.fn()
  render(
    <PaneHeader
      title="Board"
      onOpenSidebar={onOpenSidebar}
      actions={<Button>New run</Button>}
      actionsLabel="Board actions"
    />,
  )
  const heading = screen.getByRole('heading', { level: 1, name: 'Board' })
  expect(heading.tabIndex).toBe(-1)
  expect(screen.getByRole('toolbar', { name: 'Board actions' })).toBeDefined()
  fireEvent.click(screen.getByRole('button', { name: 'Open sidebar' }))
  expect(onOpenSidebar).toHaveBeenCalled()
  expect(heading.closest('header')?.className).toContain('h-11')
})

test('a run header is the taller two-line size', () => {
  render(<PaneHeader title="Fix login" size="run" stateLine={<StateLine tone="working">Running tests</StateLine>} />)
  expect(screen.getByRole('heading', { name: 'Fix login' }).closest('header')?.className).toContain('h-14')
  expect(screen.getByText('Running tests')).toBeDefined()
  expect(screen.queryByRole('toolbar')).toBeNull()
})

test('a list row is a button whose hover action shows on focus and always on a coarse pointer', () => {
  render(
    <ListRow selected trailing="3m" hoverAction={<Button size="icon-sm" label="Archive">x</Button>}>
      Fix login
    </ListRow>,
  )
  const row = screen.getByRole('button', { name: /Fix login/ })
  expect(row.getAttribute('aria-current')).toBe('true')
  expect(row.closest('[data-slot="list-row"]')?.className).toContain('coarse:h-11')
  const action = screen.getByRole('button', { name: 'Archive' }).parentElement!
  for (const token of ['hidden', 'group-focus-within/row:flex', 'group-hover/row:flex', 'coarse:flex']) {
    expect(action.className).toContain(token)
  }
})

test('an avatar is its member, in initials ringed by their colour', () => {
  expect(initials('Ada Lovelace')).toBe('AL')
  expect(initials('  ')).toBe('?')
  render(
    <>
      <Avatar name="Ada Lovelace" color="#ff0000" />
      <Avatar name="Grace Hopper" size="header" />
    </>,
  )
  const row = screen.getByRole('img', { name: 'Ada Lovelace' })
  expect(row.textContent).toBe('A')
  expect(row.style.borderColor).toBe('rgb(255, 0, 0)')
  expect(screen.getByRole('img', { name: 'Grace Hopper' }).textContent).toBe('GH')
})

test('an agent glyph is monochrome unless asked for its colour', () => {
  const { container } = render(
    <>
      <AgentGlyph agent="claude" />
      <AgentGlyph agent="claude" colored />
      <AgentGlyph agent="something-else" colored />
    </>,
  )
  const [plain, colored, unknown] = [...container.querySelectorAll('[data-slot="agent-glyph"]')].map((glyph) =>
    glyph.getAttribute('class'),
  )
  expect(plain).toContain('text-muted')
  expect(colored).toContain('text-agent-claude')
  expect(unknown).toContain('text-muted')
})

test('a labelled spinner is a status and both loaders still under reduced motion', () => {
  const { container } = render(
    <>
      <Spinner label="Loading runs" />
      <Skeleton />
    </>,
  )
  expect(screen.getByRole('status', { name: 'Loading runs' }).getAttribute('class')).toContain('motion-reduce:animate-none')
  expect(container.querySelector('[data-slot="skeleton"]')?.className).toContain('motion-reduce:animate-none')
})

test('the small text primitives render their elements', () => {
  render(
    <>
      <EmptyState title="No runs yet" action={<Button>New run</Button>}>
        Runs you launch appear here.
      </EmptyState>
      <SectionLabel as="h2">Recent</SectionLabel>
      <Kbd>K</Kbd>
      <Code>main</Code>
      <CodeBlock>go test ./...</CodeBlock>
      <Separator decorative={false} />
    </>,
  )
  expect(screen.getByRole('heading', { name: 'No runs yet' })).toBeDefined()
  expect(screen.getByRole('heading', { level: 2, name: 'Recent' }).className).toContain('text-ui-sm')
  expect(screen.getByText('K').tagName).toBe('KBD')
  expect(screen.getByText('main').tagName).toBe('CODE')
  expect(screen.getByText('go test ./...').tagName).toBe('PRE')
  expect(screen.getByRole('separator')).toBeDefined()
})
