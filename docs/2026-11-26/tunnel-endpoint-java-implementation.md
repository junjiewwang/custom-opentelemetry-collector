# Arthas Tunnel Endpoint 推导改造 — Java 探针仓库实施指引

> **状态：已实施完成（归档）**。Java SDK 侧已按本指引完成并提交
> `f5b17a0d5 feat(controlplane): Arthas Tunnel 改用 ops base_url 覆盖并收敛推导链路`，
> agent 已重新部署；`ops.arthas.tunnel_endpoint` 的解析已全部移除，collector 侧也已一次性迁移并删除该 key。
> 下文保留设计推演过程作为归档，其中「保留完整 URL key 作逃逸阀」等描述属于过渡期决策，已不再适用。
>
> 本文档供在 **Java 探针仓库** 实施时带走使用；collector 仓库侧已单独实施。
> 文档不含凭据或敏感信息。

## 1. 目标与决策

| 编号 | 决策 |
| --- | --- |
| D1 | 新增 `ops.arthas.tunnel_base_url`（单个 base URL 覆盖 scheme + host[:port] + 可选 path 前缀）；过渡期短暂保留 `ops.arthas.tunnel_endpoint`，**最终已移除** |
| D2 | 推导时按「原 URL 是否显式带 port」分情况：显式带 port 才替换为服务端下发的 `http_port`；不带 port（域名/Ingress，走默认 80/443）则结果也不带 port |
| D3 | 默认推导继续丢弃原 URL 的 path；**base_url 覆盖分支保留其 path 前缀**，在其后拼固定 `/v1/arthas/ws` |
| D4 | `derived` 来源已由探针上报，collector 侧已单独统计；探针侧无需新增上报 |
| D5 | 三处位置协作：collector（Go）→ opentelemetry-java SDK → instrumentation 打包 |

## 2. 仓库与发布链

| 位置 | 路径（本机） | 职责 |
| --- | --- | --- |
| 实现仓库 | `/root/IdeaProjects/github/opentelemetry-java`（`dev/custom`） | `sdk-extensions/controlplane/`，本指引的改动对象 |
| 打包仓库 | `/root/IdeaProjects/github/opentelemetry-java-instrumentation`（`dev/custom`） | `custom-extensions/controlplane-extension/build.gradle.kts` 依赖外部制品；**无控制面 Java 源码** |
| 服务端 | `/root/go/src/github.com/custom-opentelemetry-collector` | 已实施，见同目录 `tunnel-endpoint-derivation-implementation.md` |

发布链（据 `instrumentation/docs/2026-11-26/controlplane-auth-and-token-bridge.md`）：SDK 改动后在 SDK 仓库 `publishToMavenLocal` 覆盖 `io.opentelemetry:opentelemetry-sdk-extension-controlplane:1.57.0`，再回 instrumentation 仓库重建 javaagent，探针才会带新逻辑。**只改 SDK 不重建 javaagent 不生效。**

## 3. 现状（已确证，不要重复实现）

- `TunnelUrlGenerator.java:98-117` 的四参 `resolveEffectiveEndpoint` 已实现优先级 `local -D > ops endpoint > derived`。
- `TunnelUrlGenerator.java:167-202` 的 `generateDefaultEndpoint` 已实现 `http→ws` / `https→wss`、去掉原 path、拼 `/v1/arthas/ws`。
- `ArthasIntegration.java:353-361` 的 `resolveEndpointSource` 已返回 `local / ops / derived / none`；`:365-367` 已把 `arthas.tunnel.source` 写入心跳属性。**D4 探针侧无需改动。**
- `ControlPlaneConfig.java:470-500` 已实现「共享为默认、专属可覆盖」：`otel.agent.control.endpoint > otel.exporter.otlp.endpoint`，protocol 同理；`ControlPlaneManager.java:616` 把 `config.getEndpoint()` 传给 `ArthasConfig.setBaseOtlpEndpoint`。**无需新增 transport 抽象。**
- `ServerMetadata.java:28-29` 已有 `ops.arthas.tunnel_endpoint` / `ops.revision` 两个常量。

因此本改造**不是重写推导**，而是补一个覆盖层 + 修正端口替换逻辑。

## 4. 优先级链（过渡期设计，最终为 local -D > base_url > derived 三层）

```
1. local -D（显式 tunnelEndpoint）                    → source=local
2. ops.arthas.tunnel_endpoint（完整 ws/wss URL）      → source=ops
3. ops.arthas.tunnel_base_url（新增，部分覆盖）        → source=ops
4. 按控制面 endpoint 自动推导                          → source=derived
```

来源枚举 `local / ops / derived / none` 不变；第 3 层仍归 `ops`。删除第 2、3 层后自动回第 4 层。

## 5. 具体改动点

### 5.1 `core/model/ServerMetadata.java`

- 新增常量 `KEY_OPS_TUNNEL_BASE_URL = "ops.arthas.tunnel_base_url"`（紧邻 `:28`）。
- 新增 `@Nullable private final String opsTunnelBaseURL;`（紧邻 `:35`）。
- 构造里 `:45` 附近增加 `normalize(this.rawMetadata.get(KEY_OPS_TUNNEL_BASE_URL))`。
- 新增 getter（与 `:67` 的 `getOpsTunnelEndpoint()` 同风格）。

`normalize`（`:85`）复用即可：空串→null、trim。

### 5.2 `arthas/TunnelUrlGenerator.java`

**（a）新增 base_url 解析**：新增方法 `generateFromBaseUrl(String baseUrl)`：

| 步骤 | 规则 |
| --- | --- |
| scheme | `http → ws`、`https → wss`、`ws / wss` 原样 |
| host[:port] | 原样保留（含显式端口或不含端口两种情况） |
| path 前缀 | **保留** base_url 的 path（如 `/otel`），去掉结尾 `/` |
| 拼接 | `<base>/<v1/arthas/ws>`，即 `wss://gw.example/otel/v1/arthas/ws` |
| 校验 | 拒绝包含固定 tunnel path 的 base_url（避免重复拼接）；collector 侧 validator 已拦截，探针侧做防御性检查 |

**（b）修正 `generateDefaultEndpoint`（`:167-202`）的端口替换**：当前 `:201-202` 是

```java
if (serverHttpPort != null && serverHttpPort > 0) {
  endpoint = replacePort(endpoint, serverHttpPort);
}
```

无条件执行，域名场景（`https://collector.example`，无显式 port）会被追加 `:4318`，经 Ingress 时连不上。改为：

```java
// 仅当原始控制面 URL 显式带 port 时才做端口替换；经域名/Ingress
// 的访问由 scheme 决定默认 80/443，不能注入监听端口 4318。
if (serverHttpPort != null && serverHttpPort > 0 && hasExplicitPort(baseOtlpEndpoint)) {
  endpoint = replacePort(endpoint, serverHttpPort);
}
```

- `hasExplicitPort` 用 `URI.getPort()` 判断（`>=0` 即显式），注意 `URI.create` 抛异常时回落为 false 并记录日志。
- **默认推导分支继续丢弃原 path**（D3）；path 前缀只由 base_url 覆盖分支承载。

**（c）`resolveEffectiveEndpoint` 增加第 3 层**：所有重载（`:70`、`:98`、`:128`、`:141`）最终汇合的那个实现里，在 ops endpoint 判定之后、调用 `generateDefaultEndpoint` 之前插入：

```
if (opsTunnelBaseURL 非空) return generateFromBaseUrl(opsTunnelBaseURL);
```

注意保持 `local -D` 最高优先级；`opsTunnelBaseURL` 需要通过新增参数或 `ArthasConfig` 字段传入，避免改动过多重载签名——建议统一走 `resolveEffectiveEndpoint(ArthasConfig config, Integer serverHttpPort, ServerMetadata metadata)` 风格，或把两个 ops 值合并成一个小的 `OpsTunnelOverride` 值对象。

### 5.3 `arthas/ArthasIntegration.java`

- `:91` 附近新增 `@Nullable private volatile String opsTunnelBaseURL;`。
- `:325` 附近处理 `ServerMetadata` 更新时同步读取 `getOpsTunnelBaseURL()`；`:214` 重算 effective endpoint 时把新值传入 `resolveEffectiveEndpoint`。
- `:330` 的 `tunnelEndpointSource = resolveEndpointSource()` 保持不变（base_url 命中时仍应归 `ops`）。
- 端点变化时才触发 Tunnel 重连（沿用现有早退逻辑），避免 ops revision 变化但 URL 未变时反复重连。
- **不要修改** `resolveEndpointSource`（`:353`）与 `getOpsStatusAttributes`（`:365`）。

### 5.4 不需要改动

- `ControlPlaneConfig.java`（endpoint/protocol 已支持专属覆盖）。
- `ControlPlaneManager.java`（`setBaseOtlpEndpoint` 已传控制面 endpoint）。
- collector 的 `ops_metadata.go`（遍历 `store.Snapshot()` 下发所有 key，新增 key 自动生效）。
- instrumentation 仓库的 Java 源码（无源码；只需重构建）。

## 6. 单测清单（SDK 侧）

| 场景 | 期望 |
| --- | --- |
| http/protobuf，直连 `http://10.0.0.1:4318` + `http_port=4318` | `ws://10.0.0.1:4318/v1/arthas/ws`，source=derived |
| http/protobuf，域名 `https://collector.example`（无 port）+ `http_port=4318` | `wss://collector.example/v1/arthas/ws`，**不得出现 `:4318`**，source=derived（本批核心修复） |
| grpc，`http://10.0.0.1:4317` + `http_port=4318` | `ws://10.0.0.1:4318/v1/arthas/ws`，source=derived |
| grpc，域名 `https://grpc.example`（无 port）+ `http_port=4318` | `wss://grpc.example/v1/arthas/ws`，不注入端口 |
| base_url=`https://gw.example/otel` | `wss://gw.example/otel/v1/arthas/ws`，source=ops |
| base_url + 完整 endpoint 同时存在 | 完整 endpoint 优先 |
| local -D 与两个 ops key 同时存在 | local -D 优先，source=local |
| 删除 base_url（metadata 不再下发） | 回落到 derived |
| 老探针 / 空 metadata | 忽略未知 key，行为不变 |

## 7. 兼容性与回滚

- 老探针不认识 `ops.arthas.tunnel_base_url`，会忽略；`ops.arthas.tunnel_endpoint` 保留，灰度期可随时用完整 URL 覆盖。
- 删除 base_url 后自动回 derived，无需重启探针（metadata revision 变化触发 long-poll 唤醒）。
- 若新逻辑异常，运营端下发完整 `ops.arthas.tunnel_endpoint` 即可绕过全部推导分支。

## 8. 端到端验证口径

1. SDK 单测全绿；`publishToMavenLocal` 覆盖 `1.57.0`。
2. instrumentation 仓库重建 javaagent，确认 jar 内含新 SDK 逻辑。
3. minikube 分别以 `http/protobuf`（直连 4318 / Ingress 域名）与 `grpc`（4317）启动探针，检查 `agentRegister` 成功、Tunnel 连上、心跳 `arthas.tunnel.source=derived`。
4. 在 collector 运营端设置 `ops.arthas.tunnel_base_url`，确认在线探针收到新 metadata 并重连到 base_url + `/v1/arthas/ws`，source=ops；删除后回 derived。
5. Ops Config 页面四类来源统计（derived / ops / local / unknown）计数正确。
