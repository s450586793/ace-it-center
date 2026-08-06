import { describe, expect, it } from 'vitest'
import { isNodeOnline, networkRateLabel, percentLabel } from './status'

describe('isNodeOnline', () => {
  const now = new Date('2026-07-26T02:00:00Z')

  it('treats a node seen within 90 seconds as online', () => {
    expect(isNodeOnline('2026-07-26T01:58:30Z', now)).toBe(true)
  })

  it('treats missing or stale heartbeats as offline', () => {
    expect(isNodeOnline(null, now)).toBe(false)
    expect(isNodeOnline('2026-07-26T01:58:29Z', now)).toBe(false)
  })
})

describe('percentLabel', () => {
  it('clamps invalid percentages to the visible range', () => {
    expect(percentLabel(-4)).toBe('0%')
    expect(percentLabel(41.6)).toBe('42%')
    expect(percentLabel(120)).toBe('100%')
  })
})

describe('networkRateLabel', () => {
  it.each([
    [0, '0.00 MB/s'],
    [1.256, '1.26 MB/s'],
    [-1, '0.00 MB/s'],
    [Number.NaN, '0.00 MB/s'],
  ])('formats network rate %s', (value, expected) => {
    expect(networkRateLabel(value)).toBe(expected)
  })
})
