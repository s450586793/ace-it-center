export function isNodeOnline(lastSeenAt: string | null, now = new Date()): boolean {
  if (!lastSeenAt) return false
  const lastSeen = new Date(lastSeenAt).getTime()
  if (!Number.isFinite(lastSeen)) return false
  return now.getTime() - lastSeen <= 90_000
}

export function percentLabel(value: number): string {
  const safeValue = Number.isFinite(value) ? value : 0
  return `${Math.round(Math.min(100, Math.max(0, safeValue)))}%`
}

