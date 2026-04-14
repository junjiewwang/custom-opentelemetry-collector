# Instrumentation 页面刷新后规则丢失修复记录

## 背景与问题

在 `Instrumentation` 页面创建规则后，页面内可以立即看到新规则；但刷新页面后，`/api/v2/instrumentation/rules` 可能返回空列表，界面显示 `No Rules Found`。

已确认该问题**不是 Redis 模式失效**，也不是规则没有持久化，而是：

1. 页面刷新后没有稳定恢复当前 `app_id/service_name`
2. 当前服务列表返回顺序不稳定
3. 页面在缺少 URL 上下文时，会退回到 `serviceList[0]`
4. 当默认 service 漂移到其他服务时，规则列表接口按精确 `service_name` 过滤，自然返回空

## 本次目标

- 修复 `Instrumentation` 页面刷新后丢失当前 `app/service` 选择的问题
- 降低服务列表默认顺序漂移带来的风险
- 为服务列表顺序补回归测试

## 实施进展

### 已完成

1. **Instrumentation 页面 URL 同步**
   - 文件：`extension/adminext/webui-react/src/pages/InstrumentationPage.tsx`
   - 改动：
     - 将当前 `selectedAppId` / `selectedServiceName` 回写到 URL query
     - 页面刷新后继续依赖 `useSearchParams()` 恢复上下文
     - 切换应用时先清空旧 `service_name`，避免过渡态写入错误值

2. **服务列表稳定排序**
   - 文件：`extension/controlplaneext/servicemanager/service.go`
   - 改动：
     - 在 `ServiceManager` 层统一对服务列表做稳定排序
     - `ListServicesByApp`：按 `service_name` 稳定排序
     - `ListAllServices`：按 `app_id + service_name` 稳定排序
   - 这样前端不再依赖底层 `map/HGETALL` 的不稳定迭代顺序

3. **回归测试**
   - 文件：`extension/controlplaneext/servicemanager/service_test.go`
   - 新增测试：
     - `TestServiceService_ListServicesByApp_SortsByServiceName`
     - `TestServiceService_ListAllServices_SortsByAppThenServiceName`

## 验证结果

### Go 测试

已执行：

```bash
go test ./extension/controlplaneext/servicemanager/...
```

结果：通过。

### 前端构建

已执行：

```bash
cd extension/adminext/webui-react && npm run build
```

结果：通过。

## 影响文件

- `extension/adminext/webui-react/src/pages/InstrumentationPage.tsx`
- `extension/controlplaneext/servicemanager/service.go`
- `extension/controlplaneext/servicemanager/service_test.go`

## 预期效果

修复后：

- 在 `Instrumentation` 页面选择某个 `app/service` 后刷新页面，仍应恢复到同一个上下文
- 同一个 app 下的服务默认顺序稳定，不再因为底层返回顺序波动而随机切换
- `rules` 列表查询将继续命中正确的 `service_name`

## 未完成任务

- 暂未为 `InstrumentationPage` 增加前端自动化测试（当前项目未配置现成的页面单测框架）
- 暂未在页面显式展示“当前筛选上下文来自 URL”的提示

## 遗留观察项

- 如果后续希望进一步增强可观测性，可以在前端显式展示当前 URL 中的 `app_id/service_name`
- 若未来其他页面也依赖服务列表默认选择，可继续复用当前 `ServiceManager` 的稳定排序能力
