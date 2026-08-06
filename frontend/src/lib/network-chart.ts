import type { LineSeriesOption } from 'echarts/charts'
import type { GridComponentOption, LegendComponentOption, TooltipComponentOption } from 'echarts/components'
import type { ComposeOption } from 'echarts/core'
import type { NetworkHistoryPoint } from '../types'

export type NetworkChartOption = ComposeOption<
  LineSeriesOption | GridComponentOption | LegendComponentOption | TooltipComponentOption
>

export function buildNetworkChartOption(points: NetworkHistoryPoint[]): NetworkChartOption {
  return {
    animation: false,
    grid: { top: 48, right: 16, bottom: 36, left: 58 },
    legend: { top: 8, right: 8, itemWidth: 18, itemHeight: 2 },
    tooltip: { trigger: 'axis', valueFormatter: value => `${Number(value).toFixed(2)} MB/s` },
    xAxis: {
      type: 'time',
      axisTick: { show: false },
      axisLabel: { hideOverlap: true },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      name: 'MB/s',
      min: 0,
      axisTick: { show: false },
      splitNumber: 4,
    },
    series: [
      {
        name: '下载',
        type: 'line',
        smooth: false,
        showSymbol: false,
        lineStyle: { width: 2 },
        data: points.map(point => [point.captured_at, point.download_avg_mb_s]),
      },
      {
        name: '上传',
        type: 'line',
        smooth: false,
        showSymbol: false,
        lineStyle: { width: 2 },
        data: points.map(point => [point.captured_at, point.upload_avg_mb_s]),
      },
    ],
  }
}
