# minikube + Ingress 部署实施记录

## 需求

将本仓库 collector 部署到 minikube 集群并通过 nginx Ingress 访问。部署文件由 `service-template` 基于 `service.yaml` 生成；运行时配置由 `config-template-render` 渲染元数据产生。

## 约定与边界

- 配置链路：`collector/config/template/*.yaml`（Go Template，仅定义结构与字段引用）→ `config-template-render/config/tcs/service_provider.yaml` + `values.yaml`（唯一维护配置值的位置）→ 渲染产物 `collector/config/build/config.yaml`（不入库、不手工修改）→ K8s ConfigMap 挂载。
- 渲染机制：Stage1 `[[ .Service.* ]]`、Stage2 `{{ .Values.* }}`，sprig 函数可用；产物按模板目录相对路径 1:1 输出到同级 `build/`。
- 部署链路：`service.yaml` → `svcgen generate` → `kompose convert` → `k8s-manifests/` → `kubectl apply`；Ingress 由 `service-template` 原生生成。
- 部署形态：分布式模式，单副本；admin 保持 `api_key` 鉴权。
- 敏感值不写入本文档、不新增到代码库；统一由元数据仓库按既有方式维护。
- 渲染产物沿用 ConfigMap 挂载（用户确认）；Ingress 通过 `Host:` 请求头验证，不改系统 hosts 文件。

## 实施进展

- [x] 建立本文档，明确三仓库职责边界与实施步骤。
- [x] 优化 collector 配置模板。
- [x] 在 config-template-render 维护元数据并渲染校验产物。
- [x] 扩展 service-template 支持 Ingress，更新 collector/service.yaml。
- [x] 生成部署文件、构建镜像、部署到 minikube。
- [x] 验证访问与数据链路。

## 变更清单

| 仓库 / 文件 | 改动摘要 |
|---|---|
| `collector/config/template/config.yaml` | 删除未被任何 pipeline 引用的 `otlphttp/jaeger`、`prometheusremotewrite` exporter；VictoriaMetrics 改为 `.Service.victoriametrics` 单 endpoint + 关闭 account_scope；admin/mcp/arthas/telemetry 的密钥与 CORS 改为 `.Service` 字段引用（列表用 `toJson`） |
| `config-template-render/config/tcs/service_provider.yaml` | 新增模板所需字段段；VM 指向 minikube 内可用的单机实例；已有 Redis/ES/Nacos/COS 段保持不变 |
| `collector/config/build/config.yaml` | 渲染产物（不入库、不手工修改）；校验无缺失值、无残留占位符、YAML 可解析 |
| `service-template/pkg/config/types.go`、`validator.go` | 新增 `IngressConfig` / `IngressRuleConfig` / `IngressTLSConfig`；校验 host、class_name、path_type、service_port 必须存在于 `service.ports` |
| `service-template/pkg/generator/generator.go` | 注册并接入 Ingress 生成流程，输出到 `.tad/k8s-ingress.yaml` |
| `service-template/pkg/generator/generators/k8s/ingress/` | 新增生成器、Ingress 模板、单元测试（启用/禁用两种状态） |
| `service-template/pkg/generator/generators/build_tools/makefile/templates/makefile.tmpl` | `k8s-deploy` 自动 apply Ingress、`k8s-clean` 自动删除；ConfigMap 名称与 kompose 产物对齐；清理 help 中未实现的 target 与不存在的文档引用；kubectl v1.18 兼容（`--patch-file` → `-p`） |
| `service-template/pkg/generator/generator_test.go`、`filewriter/filewriter_test.go` | 把假阳性断言改为校验真实 target；修正既有增量写入测试的语义（用户内容应在生成标记块外） |
| `service-template/cmd/svcgen/commands/templates/service.example.yaml`、`docs/K8S_SERVICE_FEATURE.md` | 补充 Ingress 配置示例与文档 |
| `collector/service.yaml` | 新增 `local_dev.kubernetes.ingress`（nginx / 单域名 / 三条 HTTP 路由） |
| `collector/docs/2026-11-23/minikube-ingress-deployment.md` | 本实施记录 |

生成物（均不入库）：`Makefile`、`compose.yaml`、`.tad/*`、`k8s-manifests/*`、`.env.make`、`config/build/*`、前端 `dist/` 与 `node_modules/`。

## 验收记录

| 项目 | 结果 |
|---|---|
| `service-template` 全量 `go test ./...` | 通过 |
| 渲染产物校验 | 缺失字段 0、残留占位符 0、YAML 解析通过 |
| 镜像构建 | 在 `eval $(minikube docker-env)` 下构建成功，tag 与部署清单一致 |
| K8s 资源 | ConfigMap / Deployment / Service / Ingress 均创建；Pod `1/1 Running`；Service 端口名补丁成功 |
| Ingress 健康检查 | `/health` → 200 |
| Ingress Web UI | `/ui/` → 200 |
| admin 鉴权 | `/api/v2/auth/me` 无 key → 401；带 key → 200 |
| OTLP HTTP 上报 | `/v1/traces` → 200 |
| 指标落库 | VM 中可查到 collector 自监控 `otelcol_*` 序列 |
| Pod 内外部依赖 | Redis / ES 连接成功（用户授权的内网地址可达） |

## 遗留问题与风险

- 渲染器未启用 `missingkey=error`：模板字段名必须与元数据严格一致，否则静默渲染为 `<no value>`；本次通过产物校验兜底。
- `config-template-render` 的 `make render` 指向不存在的模板目录；`make setup-dirs` 会覆盖真实元数据（禁止执行）。本次未改渲染工具代码。
- 渲染产物含凭据且按用户确认沿用 ConfigMap 挂载；生产环境建议改用 Secret 挂载。
- 日志中存在 Nacos 配置中心返回的权限告警（来自既有元数据凭据/权限），不影响启动与核心链路，需元数据维护方确认。
- ES 写入出现单条字段类型冲突告警（`attributes.admin`），属既有索引映射兼容问题，不影响启动。
- `config_cross_system_demo.yaml` 会被一并渲染进 `build/` 目录，属既有行为。
- 前端 `dist/` 目前需先在本机构建，再由 `COPY . /opt/` 带入镜像；后续可改为多阶段 Dockerfile 内置 Node 构建。
- 4317(gRPC) 与 6831(UDP) 不走标准 HTTP Ingress，如需外部访问需单独 Service/NodePort 或 gRPC Ingress。
- 元数据为共享服务目录，删除 exporter 后不再引用的 `otlp_jeager` / `prometheus_server` / `jaeger_query` 段保留，不删除。
- `service.yaml` 注释中的健康检查端口与实际 admin 端口不一致，待后续清理。
