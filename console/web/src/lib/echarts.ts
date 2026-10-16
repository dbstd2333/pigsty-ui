// 按需注册 ECharts 组件，避免整包打入
import * as echarts from 'echarts/core'
import { CustomChart, LinesChart, ScatterChart } from 'echarts/charts'
import {
  DataZoomComponent,
  GridComponent,
  MarkAreaComponent,
  MarkLineComponent,
  MarkPointComponent,
  TooltipComponent,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([
  CustomChart,
  LinesChart,
  ScatterChart,
  GridComponent,
  TooltipComponent,
  DataZoomComponent,
  MarkAreaComponent,
  MarkLineComponent,
  MarkPointComponent,
  CanvasRenderer,
])

export { echarts }
