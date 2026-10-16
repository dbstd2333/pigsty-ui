import { useSettings } from '@/api/queries'

// dashboard uid 与变量名取自 files/grafana（pgsql-cluster / pgsql-instance / node-instance / pgsql-pitr）
export function useGrafana() {
  const settings = useSettings()
  const base = (settings.data?.grafanaUrl || settings.data?.derived.grafanaUrl || '').replace(/\/$/, '')
  const link = (uid: string, vars: Record<string, string>) =>
    base ? `${base}/d/${uid}?${Object.entries(vars).map(([k, v]) => `var-${k}=${encodeURIComponent(v)}`).join('&')}` : undefined
  return {
    base,
    cluster: (cls: string) => link('pgsql-cluster', { cls }),
    instance: (ins: string) => link('pgsql-instance', { ins }),
    node: (ip: string) => link('node-instance', { ip }),
    pitr: (cls: string) => link('pgsql-pitr', { cls }),
  }
}
