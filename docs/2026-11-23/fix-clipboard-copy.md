# 前端 Token 复制按钮修复记录

## 需求

修复 Application 页面（含 Token 弹窗、App 详情抽屉等）复制按钮在 HTTP 非安全上下文下点击无效、控制台抛出 `Cannot read properties of undefined (reading 'writeText')` 的问题。

## 根因

- Clipboard API 仅在安全上下文可用（HTTPS 或 localhost）。当前通过 HTTP 内网域名访问时 `navigator.clipboard` 为 `undefined`，页面直接调用 `.writeText(...)` 导致同步异常，后续成功提示也不执行。
- 项目里 `TraceDetail.tsx` 已有兼容实现（安全上下文判断 + `execCommand` 兜底），但其余页面各自重复实现且未复用，属于同一能力多处实现导致的遗漏。

## 约定与边界

- 只改前端源码，不动后端配置、渲染元数据与部署清单结构。
- 复用现有兼容实现并抽取为公共工具，统一所有复制调用点，消除重复。
- 前端产物通过 `//go:embed webui-react/dist/*` 编入二进制，必须重新构建镜像并重启 Pod 才生效。
- 文档不记录任何密钥、token 或凭据值。

## 实施进展

- [x] 建立本文档。
- [x] 抽取公共剪贴板工具并替换所有调用点。
- [x] 前端构建与类型检查。
- [x] 重建镜像并部署到 minikube。
- [x] 验证并更新本文档。

## 未完成任务

| # | 任务 | 状态 |
|---|------|------|
| 1 | 新建 `src/utils/clipboard.ts`，提供带 fallback 的统一复制能力 | 完成 |
| 2 | `AppsPage.tsx` / `AppDetailDrawer.tsx` / `DetailDrawer.tsx` 去掉裸调用 | 完成 |
| 3 | `InstancesPage.tsx` / `InstanceTasksTab.tsx` 去掉重复的 try/catch 实现 | 完成 |
| 4 | `TraceDetail.tsx` 删除本地实现，统一 import | 完成 |
| 5 | `npm run build`（tsc + vite）通过 | 完成 |
| 6 | 重建镜像、部署、Pod 重启 | 完成 |
| 7 | 通过 Ingress 验证页面与复制链路 | 完成 |

## 变更清单

| 文件 | 改动摘要 |
|---|---|
| `webui-react/src/utils/clipboard.ts` | 新增公共 `copyText()`：安全上下文走 Clipboard API（await + 捕获 rejection），HTTP 场景**同步**执行 `execCommand('copy')` 兜底；恢复原焦点与选区，返回 `Promise<boolean>` |
| `webui-react/src/pages/AppsPage.tsx` | Token 复制改用公共工具，按结果显示成功/失败 Toast |
| `webui-react/src/components/AppDetailDrawer.tsx` | 抽屉内 Token 复制改用公共工具 |
| `webui-react/src/components/DetailDrawer.tsx` | 通用字段复制改用公共工具，消除裸调用 |
| `webui-react/src/pages/InstancesPage.tsx` | 删除重复的 `try/await` 实现，改用公共工具 |
| `webui-react/src/components/InstanceTasksTab.tsx` | 同上 |
| `webui-react/src/components/TraceDetail.tsx` | 删除本地重复实现，统一 import 公共工具 |
| `webui-react/dist/*` | 重新构建产物（不入库） |
| `docs/2026-11-23/fix-clipboard-copy.md` | 本实施记录 |

源码中已无 `navigator.clipboard.writeText` 裸调用（仅公共工具内部的安全调用）。

## 验收记录

| 项目 | 结果 |
|---|---|
| `npm run build`（`tsc -b` + `vite build`） | 通过，产出新 chunk |
| IDE 诊断（改动文件） | 0 问题 |
| 源码残留裸调用 | 0 |
| 新产物含兜底逻辑 | `isSecureContext` / `execCommand` 均在构建产物中 |
| 镜像与 Pod | 在 minikube Docker 环境重建镜像，`rollout restart` 成功，Pod Running |
| Ingress 页面 | `/ui/` 200、`/health` 200、新 chunk 200；旧 chunk 仅返回 SPA HTML 回退（旧产物已移除） |
| 无头 Chrome 端到端（HTTP 非安全上下文） | `isSecureContext=False`、`navigator.clipboard=undefined`；点击 Application 页 Token 复制按钮后成功提示出现 1 次、失败提示 0 次，未再出现 `writeText` 异常 |

## 遗留问题与风险

- `npm run lint` 因项目缺少 `eslint.config.js` 无法执行（既有配置缺失，与本次改动无关）；TypeScript 构建与 IDE 诊断均通过。
- 浏览器控制台仍有一条静态资源 404（与复制逻辑无关），待后续排查。
- `document.execCommand('copy')` 为废弃 API，但仍是 HTTP 非安全上下文下唯一可用的兜底；站点启用 HTTPS 后会优先走 Clipboard API。
- 剪贴板写入依赖用户点击手势，工具函数必须在 onClick 同步链路内调用；公共工具已保证非安全分支同步执行兜底。
- 若站点嵌入 iframe，还需 `clipboard-write` 权限；本次未涉及。
- 修复只覆盖前端，HTTP 访问下其他依赖浏览器安全上下文的能力仍可能受限。
