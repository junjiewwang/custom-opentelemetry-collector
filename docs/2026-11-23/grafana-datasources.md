# Grafana 数据源配置实施记录

## 需求

在本地 Grafana 中配置 metric / trace / log 三个数据源，统一指向 collector 的兼容 API，便于直接使用 Explore。

## 方案与边界

- 复用 1Panel OpenResty 现有站点 `custom-collector.devcloud`（反代到 minikube Ingress 并固定 `Host: collector.local`）。
- 方案 A：给 Grafana 容器加 hosts 映射，使 Grafana 后端能解析该域名；不改 OpenResty 配置、不改集群 Service。
- 三个数据源统一走 collector（metric 不直连 VM）：
  - Prometheus 类型 → `/api/v2/prometheus`
  - Tempo 类型 → `/api/v2/tempo`
  - Loki 类型 → `/api/v2/loki`
- 均使用 Server(proxy) 模式与 `X-API-Key` 自定义头；值取自 `config-template-render` 元数据，通过 `secureJsonData` 加密存储。
- Grafana 认证使用用户提供的本地管理员凭据（Basic Auth），凭据不写入本文档、配置代码或命令输出。
- 不设默认数据源、不导入 Dashboard（按本轮约定）。

## 实施进展

- [x] 建立本文档。
- [x] 校验 Grafana API 认证方式并确认可用。
- [x] 给 Grafana 容器加 hosts 映射并验证解析。
- [x] 幂等创建三个数据源。
- [x] 验证数据源健康并更新本文档。

## 未完成任务

| # | 任务 | 状态 |
|---|------|------|
| 1 | 检查 Grafana API 权限 | 完成 |
| 2 | 容器 hosts 映射 + 域名解析验证 | 完成 |
| 3 | 创建/更新 Prometheus 数据源 `Collector-Metrics` | 完成 |
| 4 | 创建/更新 Tempo 数据源 `Collector-Traces` | 完成 |
| 5 | 创建/更新 Loki 数据源 `Collector-Logs` | 完成 |
| 6 | 逐个 health 验证 | 完成（Tempo 见遗留问题） |

## 变更清单

| 位置 | 改动摘要 |
|---|---|
| Grafana 容器 `/etc/hosts` | 新增 `custom-collector.devcloud` 的 hosts 映射（容器重建后需重加） |
| 本地 Grafana（HTTP API） | 新增三个数据源，均配置 `X-API-Key` 自定义头与 `secureJsonData` 加密值；原有 `jaeger` 数据源未改动 |
| `docs/2026-11-23/grafana-datasources.md` | 本实施记录 |

## 验收记录

| 项目 | 结果 |
|---|---|
| Grafana 版本 / API 认证 | 12.2.0；Basic Auth 访问 `/api/datasources` 返回 200 |
| 容器内域名解析（mount+net 命名空间） | 解析成功 |
| 容器内 `http://custom-collector.devcloud/health` | 200 |
| `Collector-Metrics` | `prometheus`，URL `/api/v2/prometheus`，proxy，`X-API-Key`；health `OK`（"Successfully queried the Prometheus API."） |
| `Collector-Logs` | `loki`，URL `/api/v2/loki`，proxy，`X-API-Key`；health `OK`（"Data source successfully connected."） |
| `Collector-Traces` | `tempo`，URL `/api/v2/tempo`，proxy，`X-API-Key`；health 接口返回 `plugin.notImplemented`（插件未实现），但 v1/v2 实际查询端点均 200 |
| Tempo 端点复核 | `/api/echo`、`/api/status/buildinfo`、`/api/search/tags`、`/api/v2/search/tags`、`/api/v2/search` 均 200 |
| 凭据处理 | 未写入文档、代码或配置明文；数据源密钥存于 Grafana `secureJsonData` |

## 遗留问题与风险

- Grafana Tempo 插件未实现 health check，UI 的 "Save & test" 会显示 `Method not implemented`；这是插件限制，不代表数据源不可用，Explore 与 TraceQL 查询可用。
- 容器 `/etc/hosts` 直接写入在容器重建后会丢失；若需持久化应改 1Panel/docker 启动配置的 `extra_hosts`。
- OpenResty 站点反代目标为 minikube IP，minikube 重建导致 IP 变化时需同步更新。
- 宿主机本身仍不解析 `custom-collector.devcloud`；浏览器访问 Grafana UI 或 collector UI 时需 hosts/DNS 映射（Grafana 容器已解析，Server 模式不受影响）。
- 数据源依赖 collector admin 的 `X-API-Key`；元数据中该值轮换后需同步更新三个数据源。
- Grafana 为 Server(proxy) 模式，浏览器不直接访问 collector，CORS 不影响。
