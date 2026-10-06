import { type ReactNode, useEffect, useRef } from 'react'
import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { javascript } from '@codemirror/lang-javascript'
import { json } from '@codemirror/lang-json'
import { markdown } from '@codemirror/lang-markdown'
import { python } from '@codemirror/lang-python'
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from '@codemirror/language'
import { toml } from '@codemirror/legacy-modes/mode/toml'
import { openSearchPanel, searchKeymap } from '@codemirror/search'
import { EditorState } from '@codemirror/state'
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from '@codemirror/view'
import { tags } from '@lezer/highlight'
import { Callout } from '@/components/ui/callout'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { FilePatch } from '@/routes/diff/patch-view'
import { parsePatch } from '@/routes/diff/parse'
import { sourceKey, type WorkspaceSource } from '@/routes/files/sources'
import { useStore } from '@/store'

export const editorCommands = {
  view: null as EditorView | null,
  openSearch() {
    if (this.view) openSearchPanel(this.view)
  },
}

const editorHighlight = HighlightStyle.define([
  { tag: tags.keyword, color: 'var(--accent-text)' },
  { tag: tags.string, color: 'var(--state-done)' },
  { tag: [tags.number, tags.bool, tags.null], color: 'var(--state-needs-you)' },
  { tag: tags.comment, color: 'var(--text-muted)', fontStyle: 'italic' },
  { tag: [tags.typeName, tags.function(tags.variableName)], color: 'var(--state-working)' },
])

const editorTheme = EditorView.theme({
  '&': { height: '100%', fontSize: '12px', backgroundColor: 'var(--canvas)', color: 'var(--text)' },
  '.cm-scroller': { overflow: 'auto', fontFamily: 'var(--font-code)' },
  '.cm-content': { padding: '8px 0', minHeight: '100%' },
  '.cm-line': { padding: '0 12px', lineHeight: '20px' },
  '.cm-gutters': { backgroundColor: 'transparent', border: 'none', color: 'var(--text-muted)' },
  '.cm-activeLineGutter': { backgroundColor: 'transparent', color: 'var(--text)' },
  '.cm-activeLine': { backgroundColor: 'var(--hover)' },
  '.cm-panels': { backgroundColor: 'var(--chrome)', color: 'var(--text)', borderColor: 'var(--seam)' },
  '@media (pointer: coarse)': {
    '.cm-panels': { maxHeight: '60%', overflowY: 'auto' },
    '.cm-panels .cm-panel.cm-search': { display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px', padding: '8px', fontSize: '13px' },
    '.cm-panels .cm-panel.cm-search > *': { margin: '0' },
    '.cm-panels .cm-panel.cm-search .cm-textfield': { boxSizing: 'border-box', minWidth: '0', maxWidth: '100%', height: '44px', fontSize: '16px', backgroundColor: 'var(--canvas)', color: 'var(--text)', border: '1px solid var(--control-border)' },
    '.cm-panels .cm-panel.cm-search .cm-textfield[name=search]': { order: '-2', width: 'calc(100% - 50px)' },
    '.cm-panels .cm-panel.cm-search .cm-button': { minWidth: '44px', minHeight: '44px', padding: '0 8px', fontSize: '13px', background: 'var(--raised)', color: 'var(--text)', border: '1px solid var(--seam)' },
    '.cm-panels .cm-panel.cm-search label': { display: 'inline-flex', alignItems: 'center', gap: '4px', minHeight: '44px', fontSize: '13px' },
    '.cm-panels .cm-panel.cm-search input[type=checkbox]': { width: '18px', height: '18px' },
    '.cm-panels .cm-panel.cm-search button[name=close]': { position: 'static', order: '-1', minWidth: '44px', minHeight: '44px' },
  },
})

function languageFor(path: string) {
  const lower = path.toLowerCase()
  if (lower.endsWith('.json') || lower.endsWith('.jsonc')) return json()
  if (lower.endsWith('.toml')) return StreamLanguage.define(toml)
  if (lower.endsWith('.md') || lower.endsWith('.markdown')) return markdown()
  if (lower.endsWith('.py')) return python()
  if (/\.(?:js|jsx|ts|tsx)$/.test(lower)) return javascript({ jsx: true, typescript: true })
  return []
}

function Notice({ children, alert = false }: { children: ReactNode; alert?: boolean }) {
  return (
    <p role={alert ? 'alert' : undefined} className={alert ? 'flex min-h-0 flex-1 items-center justify-center p-4 text-ui text-state-failed' : 'flex min-h-0 flex-1 items-center justify-center p-4 text-ui text-muted'}>
      {children}
    </p>
  )
}

export function EditableDocument({
  path,
  document,
  draft,
  canEdit,
  onChange,
  onSave,
}: {
  path: string
  document?: { content: string; truncated: boolean; binary: boolean; loading?: boolean }
  draft?: { content: string }
  canEdit: boolean
  onChange: (content: string) => void
  onSave: () => void
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const syncing = useRef(false)
  const saveRef = useRef(onSave)
  saveRef.current = onSave
  const content = draft?.content ?? document?.content ?? ''

  useEffect(() => {
    if (!host.current || !document || document.loading || document.binary || document.truncated) return
    const editor = new EditorView({
      state: EditorState.create({
        doc: content,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightSpecialChars(),
          history(),
          drawSelection(),
          rectangularSelection(),
          syntaxHighlighting(editorHighlight),
          EditorView.contentAttributes.of({ 'aria-label': `Edit ${path}` }),
          closeBrackets(),
          languageFor(path),
          keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab, { key: 'Mod-s', run: () => { saveRef.current(); return true } }]),
          highlightActiveLine(),
          EditorView.editable.of(canEdit),
          EditorState.readOnly.of(!canEdit),
          editorTheme,
          EditorView.updateListener.of((update) => {
            if (update.docChanged && !syncing.current) onChange(update.state.doc.toString())
          }),
        ],
      }),
      parent: host.current,
    })
    view.current = editor
    editorCommands.view = editor
    return () => {
      if (editorCommands.view === editor) editorCommands.view = null
      editor.destroy()
      view.current = null
    }
  }, [canEdit, document?.binary, document?.loading, document?.truncated, onChange, path])

  useEffect(() => {
    const editor = view.current
    if (!editor || editor.state.doc.toString() === content) return
    syncing.current = true
    editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: content } })
    syncing.current = false
  }, [content])

  if (!document || document.loading) return <Notice>Loading file…</Notice>
  if (document.binary) return <Notice>Binary files cannot be edited.</Notice>
  if (document.truncated) return <Notice>The server returned an incomplete file. Reload before editing.</Notice>
  return <div ref={host} className="min-h-0 flex-1 overflow-hidden" aria-label="File editor" />
}

export function DiffDocument({ selection, client, epoch }: { selection: WorkspaceSource & { path: string }; client: Api; epoch: number }) {
  const key = sourceKey(selection, selection.path)
  const state = useStore((s) => s.fileDiffs[key])
  const setFileDiff = useStore((s) => s.setFileDiff)
  const identityEpoch = useStore((s) => s.identityEpoch)
  const requestID = useRef(0)
  const stored = useStore((s) => s.diffWrap)
  const coarse = useMediaQuery(coarsePointer)
  const wrap = stored ?? coarse
  useEffect(() => {
    if (state || !selection.runID) return
    setFileDiff(key, { patch: '', truncated: false, loading: true, error: undefined })
    const pending = useStore.getState().fileDiffs[key]
    const requestToken = ++requestID.current
    const requestIdentityEpoch = identityEpoch
    const stale = () =>
      requestID.current !== requestToken ||
      useStore.getState().identityEpoch !== requestIdentityEpoch ||
      useStore.getState().fileDiffs[key] !== pending
    void client.filesDiff(selection.runID, selection.path)
      .then((result) => {
        if (!stale()) setFileDiff(key, { ...result, loading: false, error: undefined })
      })
      .catch((err) => {
        if (!stale()) setFileDiff(key, { patch: '', truncated: false, loading: false, error: message(err) })
      })
  }, [client, epoch, identityEpoch, key, selection.path, selection.runID, setFileDiff, state])
  if (!state || state.loading) return <Notice>Loading diff…</Notice>
  if (state.error) return <Notice alert>{state.error}</Notice>
  const files = parsePatch(state.patch)
  return (
    <div className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain">
      {state.truncated && (
        <Callout tone="needs-you" className="m-2">The server returned an incomplete diff. Refresh before reviewing it.</Callout>
      )}
      {files.length === 0 ? <Notice>No changes.</Notice> : files.map((file) => <FilePatch key={file.path} file={file} wrap={wrap} />)}
    </div>
  )
}
