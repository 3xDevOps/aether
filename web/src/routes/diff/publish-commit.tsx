import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { CodeBlock } from '@/components/ui/code'
import { FormField } from '@/components/ui/form-field'
import { SectionLabel } from '@/components/ui/section-label'
import { Textarea } from '@/components/ui/textarea'
import type { RunGitChange } from '@/lib/types'
import { parsePatch } from '@/routes/diff/parse'
import { FilePatch } from '@/routes/diff/patch-view'
import { Actual, Diagnostics, Heading, InlineError, Outcome } from '@/routes/diff/publish-parts'
import type { Publish } from '@/routes/diff/publish-state'

function changeMeta(change: RunGitChange): string {
  const state = change.untracked ? 'Untracked' : `Staged / index: ${change.index || '-'} · Worktree: ${change.worktree || '-'}`
  return change.conflicted ? `${state} · Conflicted` : state
}

export function CommitStep({ p, wrap, onContinue }: { p: Publish; wrap: boolean; onContinue: () => void }) {
  const status = p.status
  if (!status) return null
  const changes = status.changes ?? []
  return (
    <div className="grid min-w-0 gap-3">
      <fieldset disabled={p.busy || !!status.error} className="grid min-w-0 gap-1">
        <legend className="mb-1">
          <Heading>Changed, staged and untracked paths</Heading>
        </legend>
        {status.truncated && (
          <p role="alert" className="text-ui-sm text-state-needs-you">
            Native status is incomplete. Only the exact paths returned below can be selected.
          </p>
        )}
        {changes.length > 0 ? (
          <ul className="divide-y divide-seam rounded-panel border border-seam">
            {changes.map((change) => (
              <li key={change.path}>
                <label className="flex min-w-0 cursor-pointer items-start gap-2 px-2 py-1.5 hover:bg-hover-chrome">
                  <Checkbox
                    className="mt-0.5"
                    checked={p.paths.includes(change.path)}
                    onCheckedChange={() => p.selectPath(change.path)}
                    aria-label={`Select ${change.path}`}
                  />
                  <span className="min-w-0 text-ui-sm">
                    <span className="block font-code break-all whitespace-pre-wrap text-text">
                      {change.path}
                      {change.original_path && <span className="text-muted"> (from {change.original_path})</span>}
                    </span>
                    <span className="block text-muted">{changeMeta(change)}</span>
                  </span>
                </label>
              </li>
            ))}
          </ul>
        ) : (
          !status.error && <p className="text-ui text-muted">No native checkout changes. Anything the agent committed is ready to push.</p>
        )}
      </fieldset>
      <div className="grid justify-items-start gap-1">
        <Button
          variant="secondary"
          disabled={p.busy || !p.paths.length || !p.expected}
          onClick={() => void p.perform('review', p.reviewPaths)}
        >
          Review selected paths
        </Button>
        <InlineError>{p.errors.review}</InlineError>
      </div>
      {p.patches?.map((result, index) => (
        <section
          key={index}
          aria-label={index ? 'Selected staged diff' : 'Selected worktree diff'}
          className="min-w-0 overflow-hidden rounded-panel border border-seam"
        >
          <div className="px-2 py-1.5">
            <SectionLabel as="h3">{index ? 'Staged diff' : 'Worktree diff (tracked paths)'}</SectionLabel>
          </div>
          <div className="px-2">
            <Diagnostics output={{ ...result.output, stdout: undefined }} error={result.error} />
          </div>
          {parsePatch(result.output.stdout ?? '').map((file) => (
            <FilePatch key={file.path} file={file} wrap={wrap} lineNumbers />
          ))}
          {!result.output.stdout && !result.error && (
            <p className="px-2 pb-2 text-ui-sm text-muted">No textual changes in this comparison.</p>
          )}
        </section>
      ))}
      {p.untracked.map((preview) => (
        <section key={preview.path} aria-label={`Untracked contents: ${preview.path}`} className="grid min-w-0 gap-1">
          <SectionLabel as="h3" className="break-all">Untracked: {preview.path}</SectionLabel>
          <InlineError>{preview.error}</InlineError>
          {preview.file?.binary ? (
            <p className="text-ui-sm text-muted">Binary file; no textual preview.</p>
          ) : (
            <div className="max-h-64 overflow-y-auto">
              <CodeBlock className="whitespace-pre-wrap break-all">{preview.file?.content}</CodeBlock>
            </div>
          )}
          {preview.file?.truncated && (
            <p role="alert" className="text-ui-sm text-state-needs-you">
              Untracked preview is truncated. Review this file in the run before committing with native Git.
            </p>
          )}
        </section>
      ))}
      <FormField
        label="Commit message"
        help="Commits use the selected paths' current worktree contents, not just their staged hunks. Other staged paths stay staged. Native identity and signing apply; commit hooks do not run."
      >
        <Textarea value={p.commitMessage} onChange={(event) => p.setCommitMessage(event.target.value)} disabled={p.busy} />
      </FormField>
      <div className="grid justify-items-start gap-1">
        <Button
          disabled={p.busy || !p.canWrite || !p.caps.hasMethod('run.git.commit') || !p.reviewed || !p.commitMessage.trim()}
          onClick={() => void p.perform('commit', p.commitPaths)}
        >
          Commit selected
        </Button>
        <InlineError>{p.errors.commit}</InlineError>
      </div>
      {p.commit && (
        <Outcome label="Commit outcome">
          <p>
            Committed: {p.commit.committed ? 'yes' : 'no'} · Index updated: {p.commit.index_updated ? 'yes' : 'no'} · Hooks run:{' '}
            {p.commit.hooks_run ? 'yes' : 'no'}
          </p>
          {p.commit.head && <p className="font-code break-all">Commit HEAD: {p.commit.head}</p>}
          {p.commit.committed && !p.commit.index_updated && (
            <p role="alert" className="text-state-needs-you">
              The commit exists, but the index was not fully updated. Inspect native status; do not repeat this commit.
            </p>
          )}
          <Actual actual={p.commit.actual} />
          <Diagnostics output={p.commit.output} error={p.commit.error} />
          {p.commit.committed && (
            <div>
              <Button variant="secondary" size="sm" onClick={onContinue}>
                Continue to push
              </Button>
            </div>
          )}
        </Outcome>
      )}
    </div>
  )
}
