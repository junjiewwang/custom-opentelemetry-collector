# Arthas Terminal 行列数分析与标题显示（实施记录）

## 1. 需求与结论

排查「Terminal 交互页面的行数和列数是不是计算有问题」，并在标题上显示行列数。只读分析 + 浏览器实测结论：

- **算出的数值是对的**：5 种视口下终端既不溢出也不浪费空间；`WebSocket resize`、`.xterm-rows` 行数、xterm 实际尺寸三者始终一致。
- **算法是脆弱的经验修正**：`useTerminal.ts` 的 `adjustDimensions` 无条件 `raw.cols-1 / raw.rows-1`，用来抵消 `@xterm/addon-fit@0.11` 的几何高估。
- **UI 层真正的缺口**：`onReady` 丢弃 `(cols, rows)`、`onResize` 只发 WS，尺寸从未进入 React state，因此标题无法显示。

## 2. 分析依据（实施前实测）

`@xterm/addon-fit@0.11` 的 `proposeDimensions()` 取 `getComputedStyle(parent).width/height`；项目全局 `box-sizing: border-box`，且 xterm 容器带 `px-2 pt-2 pb-2`（8px padding），该值含 padding。它只减去 `.xterm` 自身 padding（实测 0px）并预留固定 14px overview ruler，因此高估约 1 列 / 1 行。

字符单元实测 `8.4024 × 16 px`。以 1440×900 为例：容器 `1406×813`、内容区 `1390×797`、`.xterm-screen 1378×784`；FitAddon raw `165×50`，若采用会需要 `1386px × 800px`，横向与纵向都超出可用空间。`-1/-1` 后 `164×49` 恰好放得下。

| 视口 | 容器 | FitAddon raw | 实际生效 / WS resize | 多放 1 列或 1 行是否溢出 |
| --- | --- | --- | --- | --- |
| 800×600 | 766×513 | 89×32 | 88×31 | 是 |
| 1024×768 | 990×681 | 116×42 | 115×41 | 是 |
| 1280×720 | 1246×633 | 146×39 | 145×38 | 是 |
| 1440×900 | 1406×813 | 165×50 | 164×49 | 是 |
| 1600×1000 | 1566×913 | 184×57 | 183×56 | 是 |

因此**本批不删除 `-1/-1`**（删掉会溢出），只把成因写清并常量化。

## 3. 改动

- `TerminalPanel.tsx`
  - 新增 `dimensions` state（`{cols, rows} | null`）。
  - `onReady(cols, rows)` 与 `onResize(cols, rows)` 都先 `setDimensions` 再执行原有逻辑。
  - 头部 service@ip 右侧新增 `data-testid="terminal-dimensions"` 徽标，显示 **`{cols} × {rows}`**（未就绪为 `— × —`），`title` 说明这是 xterm 实际生效并同步给 PTY 的尺寸。
  - 显示的是**调整后的值**，不是 FitAddon raw —— 否则与 PTY 实际尺寸不符。
- `useTerminal.ts`
  - `FIT_SAFETY_MARGIN = 1` 常量化，补充与字体(14px)、容器 padding(8px)、DPR=1、border-box 绑定的说明及实测依据；**行为不变**。
  - `getProposedDimensions` 接口注释明确返回已含安全余量的生效尺寸。

无其它 `terminal.fit()` 调用，所有实际尺寸变更都走 `onReady` / ResizeObserver → `onResize`，所以标题可完整覆盖。

## 4. 部署后实测（确保符合预期）

通过拦截 `WebSocket.send` 记录 `resize` 消息，并同时读标题文本与 `.xterm-rows` 子元素数：

| 场景 | 标题 | WS resize | DOM 行数 | 一致 |
| --- | --- | --- | --- | --- |
| 初始 1440×900 | `164 × 49` | `{164,49}` | 49 | ✅ |
| 缩小到 1024×768 | `115 × 41` | `{115,41}` | 41 | ✅ |
| 展开搜索框 | `115 × 38` | `{115,38}` | 38 | ✅ |
| 收起搜索框 | `115 × 41` | `{115,41}` | 41 | ✅ |
| 恢复 1440×900 | `164 × 49` | `{164,49}` | 49 | ✅ |

`title` tooltip 实测为「xterm 实际生效并同步给 PTY 的列数 × 行数（FitAddon 建议值已扣除安全余量）」。截图 `docs/2026-11-27/ui-preview/terminal-dims-title.png`。

验证过程如实记录：部署后打开 `/ui/instances` 时默认选中 `collector-self` app（该 app 无服务），需手动切到 `test003` 才出现 Arthas 按钮；这与本次改动无关。

## 5. 验证与构建

- `npm run build`、`go build ./...`、`go test -count=1 ./...` 通过，lint 0。
- 部署方式：Docker Hub 未认证拉取仍受限（429），沿用「宿主机 Go 1.25 编译 + 基于现有运行时镜像替换二进制」；前端 `dist` 通过 `go:embed` 进入二进制。`.env.make` / Dockerfile / compose 未改。

## 6. 遗留与边界

- `-1/-1` 仍是经验余量：更换字体、浏览器缩放、容器 padding、高 DPR 或升级 `@xterm/addon-fit` 后必须重新实测；根治需改为基于真实可用几何动态计算或升级 addon，本批未做。
- 未实测字体缩放与 DPR≠1 场景（当前环境 DPR=1、fontSize 固定 14）。
- 首帧 ready 后 `getProposedDimensions()` 只取值不 resize；若容器在 ResizeObserver 回调前变化，WS 可能短暂领先于 xterm，随后由 ResizeObserver 收敛。实测各场景最终值一致。

## 7. 收尾

验证用浏览器、端口转发与临时文件已清理；集群单副本 `1/1 Running`，配置模板未改动。本批改动未提交。
