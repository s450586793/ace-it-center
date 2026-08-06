import { describe, expect, it } from 'vitest'
import { beijingUsageDay, formatTrafficBytes } from './traffic'

describe('formatTrafficBytes', () => {
  it('uses decimal traffic units with stable precision', () => {
    expect(formatTrafficBytes(0)).toBe('0 B')
    expect(formatTrafficBytes(999)).toBe('999 B')
    expect(formatTrafficBytes(1_000)).toBe('1.00 KB')
    expect(formatTrafficBytes(1_500_000_000)).toBe('1.50 GB')
    expect(formatTrafficBytes(2_000_000_000_000)).toBe('2.00 TB')
  })

  it('rejects invalid counters', () => {
    expect(formatTrafficBytes(-1)).toBe('-')
    expect(formatTrafficBytes(Number.NaN)).toBe('-')
    expect(formatTrafficBytes(Number.POSITIVE_INFINITY)).toBe('-')
  })
})

describe('beijingUsageDay', () => {
  it('rolls the day at UTC+8 midnight', () => {
    expect(beijingUsageDay(new Date('2026-08-02T15:59:59Z'))).toBe('2026-08-02')
    expect(beijingUsageDay(new Date('2026-08-02T16:00:00Z'))).toBe('2026-08-03')
  })
})
