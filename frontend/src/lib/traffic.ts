const decimalUnits = ['B', 'KB', 'MB', 'GB', 'TB'] as const

export function formatTrafficBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '-'

  let scaled = value
  let unitIndex = 0
  while (scaled >= 1_000 && unitIndex < decimalUnits.length - 1) {
    scaled /= 1_000
    unitIndex += 1
  }

  if (unitIndex === 0) return `${Math.round(scaled)} B`
  return `${scaled.toFixed(2)} ${decimalUnits[unitIndex]}`
}

export function beijingUsageDay(now: Date): string {
  if (!Number.isFinite(now.getTime())) return ''
  return new Date(now.getTime() + 8 * 60 * 60 * 1_000).toISOString().slice(0, 10)
}
