import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { NetworkHistoryPoint } from '../types'
import NetworkHistoryChart from '../components/NetworkHistoryChart.vue'
import { buildNetworkChartOption } from './network-chart'

const chart = vi.hoisted(() => ({
  setOption: vi.fn(),
  resize: vi.fn(),
  dispose: vi.fn(),
}))
const initializeChart = vi.hoisted(() => vi.fn(() => chart))

vi.mock('echarts/core', () => ({
  init: initializeChart,
  use: vi.fn(),
}))
vi.mock('echarts/charts', () => ({ LineChart: {} }))
vi.mock('echarts/components', () => ({ GridComponent: {}, LegendComponent: {}, TooltipComponent: {} }))
vi.mock('echarts/renderers', () => ({ CanvasRenderer: {} }))
enableAutoUnmount(afterEach)

const point: NetworkHistoryPoint = {
  captured_at: '2026-07-28T12:00:00Z',
  upload_avg_mb_s: 1.2,
  download_avg_mb_s: 4.8,
  upload_peak_mb_s: 2.1,
  download_peak_mb_s: 8.4,
}

describe('buildNetworkChartOption', () => {
  it('builds download and upload average series in MB/s', () => {
    const option = buildNetworkChartOption([point])

    expect(option.series).toMatchObject([
      { name: '下载', type: 'line', smooth: false, data: [['2026-07-28T12:00:00Z', 4.8]] },
      { name: '上传', type: 'line', smooth: false, data: [['2026-07-28T12:00:00Z', 1.2]] },
    ])
    expect(option.yAxis).toMatchObject({ name: 'MB/s', min: 0 })
    expect(option.xAxis).toMatchObject({ axisLabel: { hideOverlap: true } })
  })
})

describe('NetworkHistoryChart', () => {
  afterEach(() => {
    vi.clearAllMocks()
  })

  it('updates, resizes, and disposes its owned chart resources', async () => {
    let resizeCallback: ResizeObserverCallback | undefined
    const resizeDisconnect = vi.fn()
    const themeDisconnect = vi.fn()
    vi.stubGlobal('ResizeObserver', vi.fn(function (callback: ResizeObserverCallback) {
      resizeCallback = callback
      return { observe: vi.fn(), disconnect: resizeDisconnect }
    }))
    vi.stubGlobal('MutationObserver', vi.fn(function () {
      return { observe: vi.fn(), disconnect: themeDisconnect }
    }))

    const wrapper = mount(NetworkHistoryChart, { props: { points: [point] } })
    await flushPromises()
    expect(initializeChart).toHaveBeenCalledOnce()
    expect(chart.setOption).toHaveBeenCalled()
    expect(chart.setOption).toHaveBeenLastCalledWith(expect.objectContaining({
      xAxis: expect.objectContaining({
        type: 'time',
        axisLine: expect.any(Object),
        axisLabel: expect.objectContaining({ hideOverlap: true }),
      }),
      yAxis: expect.objectContaining({ type: 'value', splitLine: expect.any(Object) }),
    }), true)

    resizeCallback?.([], {} as ResizeObserver)
    expect(chart.resize).toHaveBeenCalledOnce()

    await wrapper.setProps({ points: [{ ...point, download_avg_mb_s: 6.4 }] })
    expect(chart.setOption).toHaveBeenLastCalledWith(expect.objectContaining({
      series: expect.arrayContaining([expect.objectContaining({ data: [['2026-07-28T12:00:00Z', 6.4]] })]),
    }), true)

    wrapper.unmount()
    expect(resizeDisconnect).toHaveBeenCalledOnce()
    expect(themeDisconnect).toHaveBeenCalledOnce()
    expect(chart.dispose).toHaveBeenCalledOnce()
    vi.unstubAllGlobals()
  })
})
