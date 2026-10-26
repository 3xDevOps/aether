import { modeLabel } from '@/components/launch/modes'
import type { Template } from '@/lib/types'

// The same pattern as internal/templates/render.go, so the dashboard and the server agree on which names a task uses.
// RE2's \s is ASCII whitespace only, so it is spelled out.
const placeholder = /\{\{[\t\n\f\r ]*([a-zA-Z][a-zA-Z0-9_-]*)[\t\n\f\r ]*\}\}/g

export type Segment = { text: string } | { name: string; first: boolean }

/** The task split into literal text and placeholders; `first` marks each name's first occurrence. */
export function segments(task: string): Segment[] {
  const out: Segment[] = []
  const seen = new Set<string>()
  let at = 0
  for (const match of task.matchAll(placeholder)) {
    if (match.index > at) out.push({ text: task.slice(at, match.index) })
    const name = match[1]!
    out.push({ name, first: !seen.has(name) })
    seen.add(name)
    at = match.index + match[0].length
  }
  if (at < task.length) out.push({ text: task.slice(at) })
  return out
}

/** Each placeholder name once, in first-seen order. */
export function placeholderNames(task: string): string[] {
  return [...new Set(Array.from(task.matchAll(placeholder), (m) => m[1]!))]
}

/** The value set for `name`, never one inherited from Object.prototype, since {{constructor}} is a valid placeholder. */
export function own(values: Record<string, string> | undefined, name: string): string | undefined {
  return values && Object.hasOwn(values, name) ? values[name] : undefined
}

/** What a placeholder renders to: a typed value, else the template's default (which may be empty), else nothing. */
export function effective(template: Template, typed: Record<string, string>, name: string): string | undefined {
  return own(typed, name) || own(template.params, name)
}

/** The task with every filled placeholder substituted; an unfilled one stays as written. */
export function renderTask(template: Template, typed: Record<string, string>): string {
  return template.task.replace(placeholder, (match, name: string) => effective(template, typed, name) ?? match)
}

export function missingNames(template: Template, typed: Record<string, string>): string[] {
  return placeholderNames(template.task).filter((name) => effective(template, typed, name) === undefined)
}

/** The overrides template.launch takes: only values typed here that differ from the default, since an empty one would replace it. */
export function launchParams(template: Template, typed: Record<string, string>): Record<string, string> | undefined {
  const names = placeholderNames(template.task)
  const entries = names.flatMap((name) => {
    const value = own(typed, name)
    return value && value !== own(template.params, name) ? [[name, value] as const] : []
  })
  return entries.length ? Object.fromEntries(entries) : undefined
}

/** A rough size: about four characters per token, whatever the agent's tokenizer. */
export function tokenEstimate(text: string): number {
  return Math.round(text.length / 4)
}

export function matchesSearch(template: Template, agentName: string, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return [template.name, template.task, template.harness, agentName, modeLabel(template.mode), template.model ?? ''].join(' ').toLowerCase().includes(q)
}
