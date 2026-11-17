# 订阅到期检测

目标：告诉用户 E5 开发者订阅**什么时候到期、有没有续上**，到期前和状态变化时提醒。
微软没有公开“会不会续期”的判定，本功能只报告事实：日期、状态、续期记录。

## 1. 数据来源

`GET /directory/subscriptions`（companySubscription，只读）。

| 身份 | 最低权限 | 说明 |
|---|---|---|
| 委托 | `Organization.Read.All`（需要管理员同意） | 登录用户需要能读取目录；开发者租户的管理员满足 |
| 应用 | `Organization.Read.All` | 已在 `catalog.RecommendedApplication` 中 |

字段：`skuPartNumber`、`status`（`Enabled` / `Warning` / `Suspended` / `LockedOut` / `Deleted`）、
`nextLifecycleDateTime`（不续订时进入下一状态的时间）、`isTrial`、`totalLicenses`、`createdDateTime`、`id`。

- `Organization.Read.All` 加入 `catalog.RecommendedDelegated`。已有的应用注册重新运行 `e5extend app setup`（自动模式会补齐权限和同意），
  或在 Entra 里手动添加。没有权限时功能降级（§4）。
- E5 开发者订阅的识别：`skuPartNumber` 以 `DEVELOPERPACK` 开头（例如 `DEVELOPERPACK_E5`）。其他订阅也列出，但只有开发者订阅默认提醒；
  用户可以在配置里指定要关注的 SKU（`subscription.skus`）。

## 2. 何时检查

- **按租户**，不是按账号：同一租户的多个账号只查一次，用租户内第一个可用的账号的令牌。
- 引擎每 **24 小时**查一次（启动后第一轮之后进行），失败按指数退避重试（最长 6 小时一次），不影响普通调用轮次。
- `e5extend subscription [<account>]` 和 GUI 可手动刷新。

## 3. 状态与历史

`state.json` 新增 `tenants.<tenantID>.subscriptions.<id>`：最近一次看到的 `status`、`nextLifecycleDateTime`、`checkedAt`，
以及**历史**：`nextLifecycleDateTime` 每次向后移动时记一条 `{from, to, seenAt}`，即一次“续期”。最多保留 20 条。

## 4. 没有权限时

- 403/权限不足：显示“无法读取订阅信息：需要 `Organization.Read.All`（运行 `e5extend app setup` 补齐）”，不算错误、不告警。
- **手动日期**：配置 `subscription_expires = "2027-02-14"`（租户级，写在任一该租户的账号上；GUI 里在订阅卡片上填写）。
  有手动日期时照常提醒；读到真实数据后以真实数据为准，并提示可以删掉手动日期。

## 5. 提醒（通过现有通知渠道）

| 事件 | 条件 | 级别 |
|---|---|---|
| `subscription_expiring` | 关注的订阅 `nextLifecycleDateTime` 距今 ≤ 30 / 7 / 1 天时各提醒一次（同 `secret_expiring` 的分级） | 30 天 warning，7 / 1 天 critical |
| `subscription_status` | 状态从 `Enabled` 变为 `Warning` / `Suspended` / `LockedOut` / `Deleted`，或反过来恢复 `Enabled` | 变坏 critical，恢复 info |
| `subscription_renewed` | `nextLifecycleDateTime` 向后移动 | info |

这些事件加入 `config.NotifyEvents`、通知模板（中英文）、事件过滤和级别规则；冷却和免打扰规则照常适用。

## 6. 界面

- **命令行**：`e5extend subscription [<account>] [--json]`：表格列出租户、SKU、状态、到期日、剩余天数、是否试用、上次检查时间、续期历史条数；
  `doctor` 新增 `subscription.<tenant>` 检查（剩余 ≤ 7 天或状态非 Enabled 时警告；无权限时提示）。
- **桌面版**：概览页新增“E5 订阅”卡片：每个租户一行，`DEVELOPERPACK_E5 · 正常 · 2027-02-14 到期（还剩 89 天）`，
  展开显示续期历史；无权限时显示原因和“补齐权限”按钮（打开自动创建向导，复用应用注册）以及“手动填写到期日”。

## 7. 测试

假 Graph 服务：正常、即将到期（30/7/1 天各提醒一次且不重复）、状态变为 Warning 再恢复、日期后移 → 续期记录和事件、403 降级、
手动日期、同租户多账号只查一次、退避。模板渲染、i18n、GUI 卡片的 mock 变体。
