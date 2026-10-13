import { useRef, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { CodeBlock } from '@/components/ui/code'
import { copyText } from '@/lib/clipboard'
import { configPrompt } from '@/lib/config-prompt'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

const quickstart = 'https://github.com/3xDevOps/Aether/blob/main/docs/quickstart.md'

function Quickstart({ to, children }: { to: string; children: ReactNode }) {
  return (
    <a href={`${quickstart}#${to}`} target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">
      {children}
    </a>
  )
}

export function ConfigPrompt() {
  const navigate = useStore((state) => state.navigate)
  const prompt = useRef<HTMLPreElement>(null)
  return (
    <section aria-label="Agent config files" className="flex min-w-0 flex-col gap-2 text-ui text-text">
      <p>
        Paste this prompt into Claude Code, Codex, opencode, or another agent on the computer that has your
        configuration; it picks what to upload, asks you first, and uploads with the{' '}
        <code className="font-code">aether</code> CLI.
      </p>
      <p>
        That computer needs the <code className="font-code">aether</code> CLI,{' '}
        <Quickstart to="1-install">installed</Quickstart> and{' '}
        <Quickstart to="3-link-from-your-machine">linked to this server</Quickstart> with{' '}
        <code className="font-code">aether link</code>.
      </p>
      <CodeBlock
        ref={prompt}
        tabIndex={0}
        aria-label="Prompt"
        className={cn(focusRing, 'max-h-72 overflow-y-auto whitespace-pre-wrap break-words')}
      >
        {configPrompt}
      </CodeBlock>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={() => void copyText(configPrompt, prompt.current)}>
          Copy prompt
        </Button>
        <Button size="sm" variant="secondary" onClick={() => navigate('files', { home: 'open' })}>
          Open remote files
        </Button>
      </div>
    </section>
  )
}
