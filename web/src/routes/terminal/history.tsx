import { useState } from 'react'
import { Download } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'

export function TerminalHistory({ runID }: { runID: string }) {
  const [downloading, setDownloading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const download = async () => {
    if (downloading) return
    setDownloading(true)
    setError(null)
    try {
      await api.downloadTerminalHistory(runID)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Terminal history download failed.')
    } finally {
      setDownloading(false)
    }
  }

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px]">
      <Button
        type="button"
        size="sm"
        variant="outline"
        aria-label="Download full terminal history"
        disabled={downloading}
        onClick={() => void download()}
      >
        <Download aria-hidden size={14} />
        {downloading ? 'Downloading history…' : 'Download full terminal history'}
      </Button>
      <span className="min-w-0 flex-[1_1_18rem] text-muted-foreground">
        Live terminal scrollback is bounded; download the complete recorded ANSI history when you need the full transcript.
      </span>
      {error && (
        <span role="alert" className="min-w-0 basis-full break-words text-[var(--danger-soft-foreground)]">
          {error}
        </span>
      )}
    </div>
  )
}
