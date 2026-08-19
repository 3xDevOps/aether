export function ViewHeader({
  title,
  subtitle,
}: {
  title: string
  subtitle?: string
}) {
  return (
    <header className="flex items-baseline gap-2 border-b px-4 py-2">
      <h1 className="truncate text-sm font-medium">{title}</h1>
      {subtitle && (
        <span className="shrink-0 text-xs text-muted-foreground">{subtitle}</span>
      )}
    </header>
  )
}
