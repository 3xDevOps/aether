import { createContext, memo, useContext, useLayoutEffect, useMemo, useRef } from 'react'
import type * as React from 'react'
import { javascript } from '@codemirror/lang-javascript'
import { json } from '@codemirror/lang-json'
import { python } from '@codemirror/lang-python'
import { StreamLanguage, type Language } from '@codemirror/language'
import { go } from '@codemirror/legacy-modes/mode/go'
import { rust } from '@codemirror/legacy-modes/mode/rust'
import { shell } from '@codemirror/legacy-modes/mode/shell'
import { toml } from '@codemirror/legacy-modes/mode/toml'
import { yaml } from '@codemirror/legacy-modes/mode/yaml'
import { classHighlighter, highlightCode } from '@lezer/highlight'
import type { Element, ElementContent, Root } from 'hast'
import { lexer } from 'marked'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Code, CodeBlock } from '@/components/ui/code'
import { playEntrance, type Entrance } from '@/lib/motion'
import { routeFromQuery } from '@/lib/url-state'
import { cn } from '@/lib/utils'
import { FileLink } from '@/routes/files/path-link'
import { findPaths, parsePath } from '@/routes/files/paths'
import { useStore } from '@/store'
import type { Route } from '@/store/ui'

const stream = (mode: Parameters<typeof StreamLanguage.define>[0]) => () => StreamLanguage.define(mode)

const languages: Record<string, () => Language> = {
  js: () => javascript().language,
  jsx: () => javascript({ jsx: true }).language,
  ts: () => javascript({ typescript: true }).language,
  tsx: () => javascript({ typescript: true, jsx: true }).language,
  json: () => json().language,
  py: () => python().language,
  go: stream(go),
  rs: stream(rust),
  sh: stream(shell),
  toml: stream(toml),
  yaml: stream(yaml),
}

const aliases: Record<string, string> = {
  javascript: 'js',
  typescript: 'ts',
  python: 'py',
  golang: 'go',
  rust: 'rs',
  bash: 'sh',
  shell: 'sh',
  zsh: 'sh',
  console: 'sh',
  yml: 'yaml',
}

const loaded = new Map<string, Language>()

function languageFor(name: string): Language | undefined {
  const key = aliases[name] ?? name
  const make = languages[key]
  if (!make) return undefined
  let language = loaded.get(key)
  if (!language) {
    language = make()
    loaded.set(key, language)
  }
  return language
}

export function Highlighted({ code, lang }: { code: string; lang: string }) {
  const parts = useMemo(() => {
    const language = languageFor(lang)
    if (!language) return null
    const out: React.ReactNode[] = []
    highlightCode(code, language.parser.parse(code), classHighlighter, (text, classes) => {
      out.push(classes ? <span key={out.length} className={classes}>{text}</span> : text)
    }, () => out.push('\n'))
    return out
  }, [code, lang])
  return <>{parts ?? code}</>
}

/** Opens a dashboard route a link names; a surface such as the assistant's phone sheet may close itself too. */
export const OpenRoute = createContext<(route: Route) => void>((route) => useStore.getState().navigate(route.name, route.params))

/** The route a same-origin dashboard link, such as `?run=<id>`, names. */
function dashboardRoute(href: string | undefined): Route | undefined {
  if (!href) return undefined
  let url: URL
  try {
    url = new URL(href, window.location.href)
  } catch {
    return undefined
  }
  if (url.origin !== window.location.origin || !(url.searchParams.has('run') || url.searchParams.has('page'))) return undefined
  return routeFromQuery(url.searchParams)
}

function Link({ href, children }: { href?: string; children?: React.ReactNode }) {
  const open = useContext(OpenRoute)
  const route = dashboardRoute(href)
  const className = 'text-accent underline-offset-2 hover:underline'
  if (!route) return <a href={href} target="_blank" rel="noreferrer" className={className}>{children}</a>
  return (
    <a
      href={href}
      className={className}
      onClick={(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return
        event.preventDefault()
        open(route)
      }}
    >
      {children}
    </a>
  )
}

const components: Components = {
  pre: ({ children }) => <>{children}</>,
  code: ({ className, children }) => {
    const text = String(children ?? '')
    const lang = /language-([\w-]+)/.exec(className ?? '')?.[1]
    if (!lang && !text.includes('\n')) return <Code>{children}</Code>
    return (
      <CodeBlock data-lang={lang}>
        <code><Highlighted code={text.replace(/\n$/, '')} lang={lang ?? ''} /></code>
      </CodeBlock>
    )
  },
  a: ({ href, children }) => {
    const file = href ? parsePath(href) : null
    if (file) return <FileLink target={file} explicit>{children}</FileLink>
    return <Link href={href}>{children}</Link>
  },
  data: ({ value, children }) => <FileLink target={parsePath(String(value))!}>{children}</FileLink>,
}

/** Wraps each path that prose or inline code names in a `data` element, which
 * renders as a FileLink. Links and code blocks keep their text. */
function markPaths(parent: Root | Element) {
  parent.children = parent.children.flatMap((child): ElementContent[] => {
    if (child.type === 'element' && child.tagName !== 'a' && child.tagName !== 'pre') markPaths(child)
    if (child.type !== 'text') return [child as ElementContent]
    const out: ElementContent[] = []
    let at = 0
    for (const match of findPaths(child.value)) {
      const path = child.value.slice(match.start, match.end)
      out.push(
        { type: 'text', value: child.value.slice(at, match.start) },
        { type: 'element', tagName: 'data', properties: { value: path }, children: [{ type: 'text', value: path }] },
      )
      at = match.end
    }
    out.push({ type: 'text', value: child.value.slice(at) })
    return out
  })
}

const rehypePaths = () => markPaths

const Block = memo(function Block({ source }: { source: string }) {
  return <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypePaths]} components={components}>{source}</ReactMarkdown>
})

const blockEnter: Entrance = { motion: { duration: '--duration-270', easing: '--ease-enter' }, blur: true }

/** While `live`, each block that appears after the first render fades in from a blur. A `muted` text is the agent's own reasoning. */
export function Markdown({ text, live = false, muted = false }: { text: string; live?: boolean; muted?: boolean }) {
  const blocks = useMemo(() => lexer(text).map((token) => token.raw).filter((raw) => raw.trim()), [text])
  const root = useRef<HTMLDivElement>(null)
  const shown = useRef<number | null>(null)
  useLayoutEffect(() => {
    const children = root.current?.children
    if (!children) return
    if (live && shown.current !== null) {
      for (let index = shown.current; index < children.length; index++) playEntrance(children[index]!, blockEnter)
    }
    shown.current = children.length
  })
  return (
    <div ref={root} data-slot="markdown" className={cn('markdown break-words', muted ? 'text-ui-sm text-muted' : 'text-prose text-text')}>
      {blocks.map((raw, index) => <Block key={index} source={raw} />)}
    </div>
  )
}
