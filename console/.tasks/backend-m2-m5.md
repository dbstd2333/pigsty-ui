# 后端任务书：M2–M5（交给 Codex）

你继续负责 Pigsty Console 的 **Go 后端**（M0–M1 已由你完成，报告见 `console/.tasks/backend-m0-m1.report.md`）。前端由另一位工程师在 `console/web/` 并行开发：**不要创建或修改 `console/web/` 下的任何文件**；不要修改仓库中 `console/` 以外的任何文件；不要 git commit。

必读：
- `console/API-M2-M5.md`：本阶段 API 契约，**严格按此实现**（字段名、可空性、数组不返回 null、状态码、扩展字段）。
- `console/DESIGN.md` §8 动作目录、§9 作业执行、§10 已应用版本、§11 状态与漂移、§12.3/12.4 内存实际值、§13 备份与 PITR、§14 安全。
- 仓库根目录（PIGSTY_HOME）：`pgsql.yml`、`pgsql-pitr.yml`、`redis.yml`、`node.yml`、`bin/*`、`roles/pgsql/**`、`roles/infra/**`（核实 VictoriaMetrics 抓取 job 名/标签、端口），`roles/pgsql/templates/*.yml`。

## 范围

1. `internal/actions`：嵌入的 `catalog.yaml`（契约中的 MVP 动作清单）+ 参数校验（对照当前 inventory）+ 前置条件 + 命令渲染。**断言每条 ansible-playbook 命令都带 `-l`**，ad-hoc 命令都指定了具体主机。参数中的字符串一律做白名单校验（集群名/用户名/库名 `^[a-zA-Z0-9_.-]+$`，IP 为合法 IPv4），命令以 argv 数组执行，绝不拼接 shell。
2. `internal/jobs`：
   - 运行器：优先 `systemd-run --user --unit=pigsty-job-<id> --collect ...`（DESIGN §9.1），不可用时退化为 `setsid` 子进程 + PID 文件；console 重启后能接续正在运行作业的状态与日志。
   - Ansible callback 插件 `ansible/callback_plugins/pigsty_events.py`（Go embed，启动时释放到 `<data>/ansible/`），输出 ndjson 事件（DESIGN §9.2）；`no_log` 任务不输出 result。
   - 后端 tail 事件文件和 stdout，统一编号为 `JobEvent`，写入 `<data>/jobs/<id>/`，并经 SSE 推送；输出前按敏感参数值（inventory 中 sensitive 参数的实际值、`pg_users[].password` 等）脱敏。
   - 全局只允许 1 个变更作业；取消 = SIGINT，10 秒后 SIGTERM。
   - 作业记录存 SQLite（新迁移）：jobs、applied、restore_points、node_specs、settings。
   - 高风险作业前自动创建恢复点（契约 §2）。
   - 成功的非 check 作业更新 `applied`（cluster → 当前配置 commit）。
3. `internal/status`：VictoriaMetrics / Patroni REST / Alertmanager 客户端，按需取数 + 15 秒缓存，无常驻轮询；漂移检测；`/status/nodes/{ip}/memory` 的 ad-hoc 采集。地址从 inventory 推导（infra 组第一台 + 默认端口，以 `docs`/`roles` 核实），可被设置覆盖。所有外部请求 3–5 秒超时，失败只体现在 `source` 里。
4. `internal/backup`：解析 `pgbackrest info --output=json`（写单测，用真实格式的样例 JSON），计算可恢复窗口；PITR plan（选备份集、丢失区间、local 仓库限制、磁盘空间检查）；`PitrTarget` → `pg_pitr` 转换（时间统一转 `YYYY-MM-DD HH:MM:SS+00`）。
5. 设置、节点规格、已应用版本接口。
6. **模拟模式**（契约 §7）：本机（macOS 开发机）没有 ansible，也连不到任何 Pigsty 节点；前端要靠模拟模式联调，所以这部分必须做得逼真、稳定：
   - 作业事件流：按动作和目标主机生成 play/task/runner_result/stats/log，节奏真实（task 间隔 0.3–1.5s），支持取消、`fail: true` 失败。
   - 状态数据：从 inventory 生成稳定（按 IP 做种子）的节点规格与指标；PG 集群按 `pg_role` 生成 Patroni 成员（leader/replica、少量 lag），故意制造一个 role_mismatch 漂移；Redis/其他组节点也要有指标。
   - 备份：每个 PG 集群生成近 7 天的 full/incr 备份（每天 1 次 full 或 incr，保留策略合理）、archive 范围、可恢复窗口；PITR 作业成功后在 events 中加入记录。
   - 恢复点：restore-point 动作在模拟模式下也写入 restore_points 表。
7. `/meta` 增加 `simulate: boolean`。

## 质量要求

- 单元测试：动作参数校验与命令渲染（含拒绝缺少 `-l`、拒绝非法字符、拒绝对主库 rm-replica）、pgbackrest info 解析与窗口计算、PITR plan、作业并发锁、脱敏、模拟作业的事件序列完整性。
- `go vet ./...`、`go test ./...` 全部通过。
- 空闲内存目标仍 < 40MB；SSE 连接断开要释放 goroutine。
- 代码风格与已有代码一致：简洁、包内聚、注释适度。

## 完成后

1. 写 `console/.tasks/backend-m2-m5.report.md`：实现了什么、与契约的任何偏差（原则上不应有；如有必须列出）、哪些行为需要在真实沙箱上核实、已知问题。
2. 在 console 目录下用 `go run ./cmd/pigsty-console serve --pigsty-home .. --data-dir ./.data --simulate --listen 127.0.0.1:8861` 实际跑一遍（**用 8861 端口**，8860 上有用户自己的实例，不要动它），用 curl 走通：actions 列表、plan（含一个 422 与一个带 gate 的）、提交作业、SSE 读到 end、jobs 列表、cancel、applied、status/nodes、status/clusters、drift、backups、pitr plan、settings、node-specs。把摘要写进报告，验证完停止你启动的服务。
3. 确认 `pigsty.yml` 字节未变。
