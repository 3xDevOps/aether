import { readFileSync, readdirSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { render } from '@testing-library/react'
import { createElement } from 'react'
import { describe, expect, it } from 'vitest'
import { Check } from '@/components/icons'

// A rule's `legacy` list names files that predate it. The test also fails when
// a listed file no longer breaks the rule, so the lists only shrink.

const src = path.dirname(fileURLToPath(import.meta.url))

const sources = readdirSync(src, { recursive: true, encoding: 'utf8' })
  .filter((file) => /\.tsx?$/.test(file) && !/\.test\.tsx?$/.test(file) && !file.startsWith('test/'))
  .map((file) => ({ file: file.split(path.sep).join('/'), text: readFileSync(path.join(src, file), 'utf8') }))

const primitiveNames = sources
  .filter(({ file }) => file.startsWith('components/ui/'))
  .flatMap(({ text }) => [
    ...[...text.matchAll(/^export (?:function|const) ([A-Z]\w*)/gm)].map((m) => m[1]),
    ...[...text.matchAll(/^export \{([^}]*)\}/gm)].flatMap((m) => m[1].match(/\b[A-Z]\w*/g) ?? []),
  ])

type Rule = {
  name: string
  why: string
  pattern: RegExp
  applies?: (file: string) => boolean
  legacy: string[]
}

const rules: Rule[] = [
  {
    name: 'type scale',
    why: 'Use text-ui-xs, text-ui-sm, text-ui, text-prose or text-title; no other sizes.',
    pattern: /\btext-(?:xs|sm|base|lg|xl|[2-9]xl)\b|\btext-\[\d+(?:\.\d+)?(?:px|rem|em)\]/,
    legacy: [
      'components/cli-update-banner.tsx',
      'components/connection-error.tsx',
      'components/copyable-command.tsx',
      'components/dock.tsx',
      'components/feed-entry.tsx',
      'components/missing-run.tsx',
      'components/palette/close-dialog.tsx',
      'components/palette/forward-dialog.tsx',
      'components/palette/inject-dialog.tsx',
      'components/palette/palette.tsx',
      'components/profile-import.tsx',
      'components/run-header.tsx',
      'components/run-input-indicator.tsx',
      'components/run-list.tsx',
      'components/shortcuts/index.tsx',
      'components/terminal-image.tsx',
      'components/terminal-keys.tsx',
      'components/terminal-pane.tsx',
      'components/ui/command.tsx',
      'components/update-banner-shared.tsx',
      'components/update-banner.tsx',
      'components/workspace-create.tsx',
      'components/workspace-mirror-dialog.tsx',
      'components/workspace-repository.tsx',
      'routes/admin-dialogs/budget-dialog.tsx',
      'routes/admin-dialogs/import-repository-dialog.tsx',
      'routes/admin-dialogs/workspace-settings-dialog.tsx',
      'routes/agents/index.tsx',
      'routes/agents/wizard.tsx',
      'routes/board/clear-done-dialog.tsx',
      'routes/board/index.tsx',
      'routes/board/member-avatar.tsx',
      'routes/board/run-card.tsx',
      'routes/board/run-map.tsx',
      'routes/board/stop-environment-dialog.tsx',
      'routes/board/terminal-dock.tsx',
      'routes/browser/index.tsx',
      'routes/browser/surface.tsx',
      'routes/configuration/index.tsx',
      'routes/devices/index.tsx',
      'routes/diff/conflict-chips.tsx',
      'routes/diff/index.tsx',
      'routes/diff/land.tsx',
      'routes/diff/native-changes.tsx',
      'routes/diff/patch-view.tsx',
      'routes/diff/review-commands.tsx',
      'routes/files/index.tsx',
      'routes/members/index.tsx',
      'routes/members/invitations.tsx',
      'routes/members/personal.tsx',
      'routes/missions/index.tsx',
      'routes/missions/phase.tsx',
      'routes/onboarding/agents-step.tsx',
      'routes/onboarding/edge-link.tsx',
      'routes/onboarding/git-identity-step.tsx',
      'routes/onboarding/github-connect.tsx',
      'routes/onboarding/index.tsx',
      'routes/onboarding/repo-step.tsx',
      'routes/onboarding/source-option.tsx',
      'routes/onboarding/steps.tsx',
      'routes/run-sync/sync-panel.tsx',
      'routes/settings/index.tsx',
      'routes/settings/usage.tsx',
      'routes/team/approvals.tsx',
      'routes/team/presence.tsx',
      'routes/team/timeline.tsx',
      'routes/templates/index.tsx',
      'routes/templates/schedule-editor.tsx',
      'routes/terminal/candidate-review.tsx',
      'routes/terminal/events.tsx',
      'routes/terminal/evidence-drawer.tsx',
      'routes/terminal/history.tsx',
      'routes/terminal/index.tsx',
      'routes/terminal/run-dock.tsx',
      'routes/terminal/run-room.tsx',
      'routes/terminal/tabs.tsx',
      'routes/terminal/takeover-dialog.tsx',
      'routes/workspace.tsx',
      'routes/workspaces/index.tsx',
    ],
  },
  {
    name: 'token colours',
    why: 'Colour comes from the tokens in index.css, never the Tailwind palette.',
    pattern:
      /\b(?:bg|text|border(?:-[trblxy])?|ring|outline|fill|stroke|from|via|to|divide|decoration|placeholder|caret|shadow)-(?:(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d{2,3}|black|white)\b|\b(?:bg|text|border|ring|fill|stroke|outline|decoration|from|via|to)-\[(?:#|rgb|hsl|oklch)/,
    legacy: [
      'routes/browser/surface.tsx',
    ],
  },
  {
    name: 'no HeroUI',
    why: 'HeroUI is gone; use the components/ui primitives.',
    pattern: /from\s+['"]@heroui\//,
    legacy: [],
  },
  {
    name: 'icons through components/icons',
    why: 'Import icons from @/components/icons, which sets the stroke; add a name there if one is missing.',
    pattern: /from\s+['"]lucide-react['"]/,
    applies: (file) => file !== 'components/icons.ts',
    legacy: [
      'components/cli-update-banner.tsx',
      'components/connection-error.tsx',
      'components/copyable-command.tsx',
      'components/dock.tsx',
      'components/missing-run.tsx',
      'components/palette/close-dialog.tsx',
      'components/palette/palette.tsx',
      'components/run-actions.tsx',
      'components/run-header.tsx',
      'components/run-input-indicator.tsx',
      'components/terminal-image.tsx',
      'components/terminal-keys.tsx',
      'components/terminal-pane.tsx',
      'components/ui/command.tsx',
      'components/update-banner-shared.tsx',
      'components/update-banner.tsx',
      'lib/commands.ts',
      'routes/board/clear-done-dialog.tsx',
      'routes/board/index.tsx',
      'routes/board/run-card.tsx',
      'routes/board/run-map.tsx',
      'routes/diff/conflict-chips.tsx',
      'routes/diff/index.tsx',
      'routes/diff/land.tsx',
      'routes/files/index.tsx',
      'routes/members/index.tsx',
      'routes/missions/index.tsx',
      'routes/onboarding/index.tsx',
      'routes/run-sync/sync-panel.tsx',
      'routes/settings/index.tsx',
      'routes/settings/usage.tsx',
      'routes/team/approvals.tsx',
      'routes/terminal/evidence-drawer.tsx',
      'routes/terminal/index.tsx',
      'routes/terminal/run-dock.tsx',
      'routes/terminal/run-room.tsx',
    ],
  },
  {
    name: 'one name per icon',
    why: 'These are lucide aliases of icons components/icons exports under their current names.',
    pattern: /\b(?:XIcon|CheckIcon|SearchIcon|ChevronDownIcon|ChevronRightIcon|ChevronUpIcon|AlertTriangle|Loader2|MoreHorizontal|RefreshCwIcon|GitCommit)\b/,
    legacy: [
      'components/cli-update-banner.tsx',
      'components/run-actions.tsx',
      'components/terminal-image.tsx',
      'components/terminal-pane.tsx',
      'components/ui/command.tsx',
      'routes/board/clear-done-dialog.tsx',
      'routes/board/run-card.tsx',
      'routes/settings/usage.tsx',
    ],
  },
  {
    name: 'no bare buttons',
    why: 'Use Button from components/ui so every button shares one look and focus ring.',
    pattern: /<button\b/,
    applies: (file) => !file.startsWith('components/ui/'),
    legacy: [
      'components/dock.tsx',
      'components/feed-entry.tsx',
      'components/run-input-indicator.tsx',
      'components/run-list.tsx',
      'routes/board/run-card.tsx',
      'routes/diff/conflict-chips.tsx',
      'routes/diff/index.tsx',
      'routes/diff/native-changes.tsx',
      'routes/files/index.tsx',
      'routes/members/personal.tsx',
      'routes/missions/index.tsx',
      'routes/onboarding/index.tsx',
      'routes/run-sync/sync-panel.tsx',
      'routes/settings/usage.tsx',
      'routes/team/approvals.tsx',
      'routes/terminal/evidence-drawer.tsx',
      'routes/terminal/run-room.tsx',
      'routes/terminal/tabs.tsx',
    ],
  },
  {
    name: 'no opacity on rows',
    why: 'A receding row switches its text to text-muted; opacity drops contrast below 4.5:1.',
    pattern: /\bopacity-\d/,
    applies: (file) => file.startsWith('components/shell/') || file.startsWith('routes/board/'),
    legacy: [
      'routes/board/run-map.tsx',
    ],
  },
  {
    name: 'primitives are not restyled',
    why: "Pass a primitive's variant or size; className is for layout only (margin, width, flex).",
    pattern: new RegExp(
      `<(?:${primitiveNames.join('|')})\\b(?:=>|[^<>])*?\\bclassName=(?:\\{(?:cn\\()?)?["'\`][^"'\`]*?\\b(?:text|bg|border|rounded|h|min-h|p|px|py)-`,
    ),
    applies: (file) => !file.startsWith('components/ui/'),
    legacy: [
      'components/cli-update-banner.tsx',
      'components/connection-error.tsx',
      'components/missing-run.tsx',
      'components/palette/close-dialog.tsx',
      'components/palette/forward-dialog.tsx',
      'components/palette/inject-dialog.tsx',
      'components/palette/palette.tsx',
      'components/run-actions.tsx',
      'components/run-command-confirmation.tsx',
      'components/run-list.tsx',
      'components/shortcuts/index.tsx',
      'components/terminal-image.tsx',
      'components/terminal-keys.tsx',
      'components/terminal-pane.tsx',
      'components/update-banner.tsx',
      'components/workspace-mirror-dialog.tsx',
      'components/workspace-repository.tsx',
      'routes/admin-dialogs/budget-dialog.tsx',
      'routes/admin-dialogs/import-repository-dialog.tsx',
      'routes/admin-dialogs/workspace-settings-dialog.tsx',
      'routes/agents/index.tsx',
      'routes/board/clear-done-dialog.tsx',
      'routes/board/index.tsx',
      'routes/board/run-card.tsx',
      'routes/browser/index.tsx',
      'routes/browser/surface.tsx',
      'routes/devices/index.tsx',
      'routes/diff/conflict-chips.tsx',
      'routes/diff/index.tsx',
      'routes/diff/review-commands.tsx',
      'routes/members/index.tsx',
      'routes/members/invitations.tsx',
      'routes/missions/index.tsx',
      'routes/onboarding/agents-step.tsx',
      'routes/onboarding/git-identity-step.tsx',
      'routes/onboarding/repo-step.tsx',
      'routes/onboarding/steps.tsx',
      'routes/settings/index.tsx',
      'routes/team/timeline.tsx',
      'routes/templates/index.tsx',
      'routes/terminal/control-button.tsx',
      'routes/terminal/evidence-drawer.tsx',
      'routes/terminal/index.tsx',
      'routes/terminal/run-dock.tsx',
      'routes/terminal/run-room.tsx',
      'routes/workspaces/index.tsx',
    ],
  },
  {
    // index.css aliases these misspellings to the real state colours so they
    // render; until this list is empty they are still wrong names.
    name: 'real state names',
    why: 'state-attention, state-success and state-warn are misspellings: use state-needs-you or state-done.',
    pattern: /\bstate-(?:attention|success|warn)\b/,
    legacy: [
      'components/workspace-mirror-dialog.tsx',
      'routes/missions/index.tsx',
      'routes/missions/phase.tsx',
      'routes/terminal/candidate-review.tsx',
      'routes/terminal/run-room.tsx',
    ],
  },
]

describe('design system source scan', () => {
  it('finds the primitives and the source', () => {
    expect(primitiveNames).toContain('Button')
    expect(sources.length).toBeGreaterThan(100)
  })

  for (const rule of rules) {
    it(rule.name, () => {
      const breaking = sources
        .filter(({ file, text }) => (rule.applies?.(file) ?? true) && rule.pattern.test(text))
        .map(({ file }) => file)
      const added = breaking.filter((file) => !rule.legacy.includes(file))
      const fixed = rule.legacy.filter((file) => !breaking.includes(file))
      expect(added, `${rule.why} Fix these files.`).toEqual([])
      expect(fixed, `These files no longer break "${rule.name}": delete them from its legacy list.`).toEqual([])
    })
  }
})

describe('icons', () => {
  it('draws at stroke 1.75 unless the caller asks otherwise', () => {
    const { container } = render(createElement('div', null, createElement(Check), createElement(Check, { strokeWidth: 2 })))
    const [standard, custom] = container.querySelectorAll('svg')
    expect(standard.getAttribute('stroke-width')).toBe('1.75')
    expect(custom.getAttribute('stroke-width')).toBe('2')
  })
})

// WCAG 2.x contrast, read from index.css.
const css = readFileSync(path.join(src, 'index.css'), 'utf8')

function themeColours(selector: string): Record<string, string> {
  const block = css.match(new RegExp(`^${selector} \\{\\n([\\s\\S]*?)^\\}`, 'm'))?.[1]
  if (!block) throw new Error(`index.css has no "${selector} {" token block`)
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-f]{6});/gi)].map((m) => [m[1], m[2]]))
}

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const states = ['state-working', 'state-needs-you', 'state-failed', 'state-done', 'state-paused']
const pairs: [fg: string, bg: string, min: number][] = [
  ...['text', 'text-muted'].flatMap((fg) =>
    ['canvas', 'chrome', 'raised', 'hover', 'hover-chrome'].map((bg): [string, string, number] => [fg, bg, 4.5]),
  ),
  ['text', 'selection', 4.5],
  ['accent-text', 'canvas', 4.5],
  ['accent-text', 'chrome', 4.5],
  ['on-accent', 'accent-fill', 4.5],
  ['on-failed', 'state-failed', 4.5],
  ...[...states, 'diff-add', 'diff-del'].map((fg): [string, string, number] => [fg, 'canvas', 4.5]),
  ['control-border', 'canvas', 3],
  ['icon-faint', 'canvas', 3],
  ...['claude', 'codex', 'pi', 'omp', 'opencode'].map((agent): [string, string, number] => [`agent-${agent}`, 'canvas', 3]),
]

describe.each([
  ['light', ':root'],
  ['dark', '\\.dark'],
])('%s theme contrast', (_, selector) => {
  const colours = themeColours(selector)
  it.each(pairs)('%s on %s', (fg, bg, min) => {
    expect(colours[fg], `--${fg} is not a hex colour in ${selector}`).toMatch(/^#/)
    expect(colours[bg], `--${bg} is not a hex colour in ${selector}`).toMatch(/^#/)
    expect(contrast(colours[fg], colours[bg])).toBeGreaterThanOrEqual(min)
  })
})

describe('primitive colour utilities', () => {
  const defined = new Set([...css.matchAll(/^\s*--((?:color|text-color|background-color|text)-[\w-]+):/gm)].map((m) => m[1]))
  const utilities = new Set([...css.matchAll(/^@utility ([\w-]+)/gm)].map((m) => m[1]))
  const builtIn = new Set(['transparent', 'current', 'inherit', 'white', 'black', 'left', 'center', 'right'])
  const primitives = sources.filter(({ file }) => file.startsWith('components/ui/')).map(({ file, text }) => [file, text])
  it.each(primitives)('%s names only defined tokens', (_, text) => {
    const missing = [...text.matchAll(/(?<![\w-])(text|bg)-([a-z][\w-]*)/g)]
      .filter(([match, prop, name]) => {
        if (utilities.has(match) || builtIn.has(name) || defined.has(`color-${name}`)) return false
        return prop === 'bg' ? !defined.has(`background-color-${name}`) : !defined.has(`text-color-${name}`) && !defined.has(`text-${name}`)
      })
      .map(([match]) => match)
    expect(missing, 'Tailwind generates no class for these; add the token to index.css').toEqual([])
  })
})
