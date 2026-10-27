/** crypto.randomUUID exists only in a secure context; a dashboard served over plain HTTP on a LAN has none. */
export function randomID(prefix: string): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2)}`
}
