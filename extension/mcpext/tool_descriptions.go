// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

// Shared tool description fragments.
//
// All Arthas command tools run through the Control Plane task chain. They do
// not require a Tunnel registration: the task carries auto_attach=true and
// require_tunnel_ready=false. Keeping one copy of this text prevents the tool
// descriptions from drifting away from the actual execution model again.
const (
	commandPathDescription = "执行链路：通过 Control Plane 任务链路下发结构化命令任务，" +
		"由目标 Java Agent 内的命令桥执行并回传结果。命令任务自带 auto_attach，无需 Tunnel 注册；" +
		"只需目标 Agent 在线。Tunnel 注册仅影响交互式 Web Terminal。"

	statusToolDescription = "查询目标 Agent 的 Arthas 状态，返回三个正交维度：" +
		"agent_online（Agent 是否在册）、arthas_runtime（JVM 内 Runtime：RUNNING/STOPPED/UNKNOWN）、" +
		"tunnel_registered（是否注册了 Tunnel WS），以及活跃会话与空闲回收提示。" +
		"tunnel_not_registered 不代表 attach 失败，也不影响结构化命令执行。"

	attachToolDescription = "向目标 Agent 下发 arthas_attach 任务，用于预热 JVM 内 Arthas Runtime " +
		"或建立交互式 Web Terminal 所需的 Tunnel 会话。命令工具自带 auto_attach，本工具不是命令前置条件。" +
		"任务为两段式：先返回 PENDING_CONFIRMATION，Runtime 就绪可用 arthas_status 或 arthas_exec('version') 确认；" +
		"tunnel_registered 只作为 Terminal 可用的附加条件。"
)
