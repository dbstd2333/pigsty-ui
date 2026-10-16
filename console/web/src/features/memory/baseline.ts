// 各组件常驻内存经验值（MB），Pigsty v4.5.0。全部为待校准的初值：
// 可在容量页用实测值（systemctl MemoryCurrent）覆盖，覆盖值保存在后端设置 memoryBaseline 中。
export const BASELINE = {
  node_exporter: { label: 'node_exporter', mb: 30, service: 'node_exporter' },
  vector: { label: 'vector 日志采集', mb: 80, service: 'vector' },
  chronyd: { label: 'chronyd', mb: 5, service: 'chronyd' },
  haproxy: { label: 'haproxy', mb: 30, service: 'haproxy' },
  vip_manager: { label: 'vip-manager', mb: 15, service: 'vip-manager' },
  pg_background: { label: 'PG 后台进程', mb: 120, service: undefined },
  patroni: { label: 'patroni', mb: 60, service: 'patroni' },
  pgbouncer: { label: 'pgbouncer', mb: 20, service: 'pgbouncer' },
  pg_exporter: { label: 'pg_exporter', mb: 40, service: 'pg_exporter' },
  pgbackrest: { label: 'pgbackrest（备份时）', mb: 200, service: undefined },
  etcd: { label: 'etcd', mb: 150, service: 'etcd' },
  minio: { label: 'minio / silo', mb: 300, service: 'minio' },
  victoria_metrics: { label: 'VictoriaMetrics', mb: 500, service: 'victoria-metrics' },
  victoria_logs: { label: 'VictoriaLogs', mb: 300, service: 'victoria-logs' },
  grafana: { label: 'Grafana', mb: 150, service: 'grafana-server' },
  alertmanager: { label: 'Alertmanager', mb: 40, service: 'alertmanager' },
  nginx: { label: 'Nginx', mb: 30, service: 'nginx' },
  blackbox_exporter: { label: 'blackbox_exporter', mb: 20, service: 'blackbox_exporter' },
  console: { label: 'Pigsty Console', mb: 40, service: 'pigsty-console' },
  redis_base: { label: 'Redis 进程基础开销', mb: 10, service: undefined },
  docker_apps: { label: 'Docker 应用（请按实际填写）', mb: 0, service: undefined },
} satisfies Record<string, { label: string; mb: number; service: string | undefined }>

export type BaselineId = keyof typeof BASELINE
