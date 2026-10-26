import { useRef, useState, type RefObject } from 'react'
import { ScrollText } from '@/components/icons'
import { renderTask } from '@/components/templates/placeholders'
import { TemplatePicker } from '@/components/templates/template-picker'
import { keepEscapeForSearch } from '@/components/templates/template-list'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { useIsMobile } from '@/lib/breakpoints'
import { surfaces } from '@/lib/surfaces'
import { composerDraft, saveComposerDraft } from '@/routes/run/composer-state'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** The draft with `text` in place of the textarea's selection, or after a blank line when nothing is selected. */
export function insertAt(body: string, text: string, start: number, end: number): { body: string; caret: number } {
  if (start !== end) return { body: body.slice(0, start) + text + body.slice(end), caret: start + text.length }
  const before = body.trimEnd() ? `${body.trimEnd()}\n\n` : ''
  return { body: before + text, caret: before.length + text.length }
}

/** The composer's "Insert a template" button: fills a template and puts it in the draft; it never sends. */
export function TemplateInsert({ run, textarea, disabled }: { run: RunRecord; textarea: RefObject<HTMLTextAreaElement | null>; disabled: boolean }) {
  const cap = useCapability()
  const phone = useIsMobile()
  const navigate = useStore((s) => s.navigate)
  const [open, setOpen] = useState(false)
  const caret = useRef<number | null>(null)
  if (!cap.hasMethod('template.list')) return null

  const library = surfaces(cap).some((s) => s.name === 'templates')
  const restoreFocus = (event: Event) => {
    const at = caret.current
    caret.current = null
    // A click elsewhere closed the picker and already moved focus there; keep it.
    if (at === null && document.activeElement && document.activeElement !== document.body) return
    event.preventDefault()
    const el = textarea.current
    el?.focus()
    if (el && at !== null) requestAnimationFrame(() => el.setSelectionRange(at, at))
  }
  const picker = (
    <TemplatePicker
      workspaceID={run.workspace_id}
      onOpenTemplates={library ? () => navigate('templates') : undefined}
      action={(template, typed) => ({
        text: 'Insert',
        run: () => {
          const draft = composerDraft(run.id)
          if (draft.busy) return
          const el = textarea.current
          const next = insertAt(draft.body, renderTask(template, typed), el?.selectionStart ?? draft.body.length, el?.selectionEnd ?? draft.body.length)
          saveComposerDraft(run.id, { body: next.body })
          caret.current = next.caret
          setOpen(false)
        },
      })}
    />
  )
  const trigger = (
    <Button variant="ghost" size="icon-sm" label="Insert a template" disabled={disabled}>
      <ScrollText />
    </Button>
  )

  if (phone) {
    return (
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogTrigger asChild>{trigger}</DialogTrigger>
        <DialogContent className="flex h-[min(36rem,85dvh)] flex-col gap-0 p-0 max-md:pb-0 [&_[data-slot=prompt-footer]]:pb-[env(safe-area-inset-bottom)]" onCloseAutoFocus={restoreFocus} onEscapeKeyDown={keepEscapeForSearch}>
          <DialogHeader className="px-4 pt-4 pb-2">
            <DialogTitle>Insert a template</DialogTitle>
          </DialogHeader>
          {picker}
        </DialogContent>
      </Dialog>
    )
  }
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent
        side="top"
        aria-label="Insert a template"
        className="flex h-[min(440px,60vh)] w-[min(622px,calc(100vw-16px))] flex-col overflow-hidden p-0"
        onCloseAutoFocus={restoreFocus}
        onEscapeKeyDown={keepEscapeForSearch}
      >
        {picker}
      </PopoverContent>
    </Popover>
  )
}
