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
    legacy: [],
  },
  {
    name: 'token colours',
    why: 'Colour comes from the tokens in index.css, never the Tailwind palette.',
    pattern:
      /\b(?:bg|text|border(?:-[trblxy])?|ring|outline|fill|stroke|from|via|to|divide|decoration|placeholder|caret|shadow)-(?:(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d{2,3}|black|white)\b|\b(?:bg|text|border|ring|fill|stroke|outline|decoration|from|via|to)-\[(?:#|rgb|hsl|oklch)/,
    legacy: [],
  },
  {
    name: 'sentence case',
    why: 'Labels are sentence case; use SectionLabel for a group heading, never uppercase.',
    pattern: /\buppercase\b/,
    legacy: [],
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
    legacy: [],
  },
  {
    name: 'one name per icon',
    why: 'These are lucide aliases of icons components/icons exports under their current names.',
    pattern: /\b(?:XIcon|CheckIcon|SearchIcon|ChevronDownIcon|ChevronRightIcon|ChevronUpIcon|AlertTriangle|Loader2|MoreHorizontal|RefreshCwIcon|GitCommit)\b/,
    legacy: [],
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
      'routes/diff/conflict-chips.tsx',
      'routes/team/approvals.tsx',
    ],
  },
  {
    name: 'no opacity on rows',
    why: 'A receding row switches its text to text-muted; opacity drops contrast below 4.5:1.',
    pattern: /\bopacity-\d/,
    applies: (file) => file.startsWith('components/shell/') || file.startsWith('routes/board/'),
    legacy: [],
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
      'components/palette/close-dialog.tsx',
      'components/palette/forward-dialog.tsx',
      'components/palette/inject-dialog.tsx',
      'components/palette/palette.tsx',
      'components/run-list.tsx',
      'components/shortcuts/index.tsx',
      'components/terminal-image.tsx',
      'components/terminal-keys.tsx',
      'components/update-banner.tsx',
      'components/workspace-mirror-dialog.tsx',
      'routes/admin-dialogs/budget-dialog.tsx',
      'routes/admin-dialogs/import-repository-dialog.tsx',
      'routes/admin-dialogs/workspace-settings-dialog.tsx',
    ],
  },
  {
    name: 'token names',
    why: 'These colour names are not tokens and render nothing; use the names in index.css (docs/styles.md).',
    pattern:
      /(?<![\w-])(?:bg|text|border(?:-[trblxy])?|ring|outline|divide|fill|stroke|placeholder)-(?:background|foreground|card|popover|sidebar|primary|secondary|muted-foreground|destructive|input|ring|border|toolbar-hover|field|state-(?:attention|success|warn|waiting|needs-attention|idle))\b/,
    legacy: [],
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
