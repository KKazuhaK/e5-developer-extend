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

## 8. 实现说明

- **租户键**：`state.json` 的 `tenants.<键>` 里的键起初是账号 `tenant` 配置的小写形式；`organizations` / `common` / `consumers` 不指定具体租户，这样的账号单独成一个租户（键为 `account:<账号名>`）。令牌里的租户 GUID（`tid`）一出现，这些键就记成别名（`tenant_aliases`）并把状态合并到 GUID 下，所以用域名、GUID 和 `organizations` 写同一个租户的账号只读一次；检查一次只做一个，后一个发现已有人读过就跳过。
- **用哪个账号**：按配置顺序，跳过暂停、自动暂停和运行期限已结束的账号（手动刷新时也尝试需要处理的账号）；某个账号被拒绝（401/403）或取不到令牌就换下一个，全部被拒才算“没有权限”。
- **时间**：状态里记 `attempted_at`；下次检查 = 上次尝试 + 24 小时（失败 + 退避，5 分钟起翻倍，最长 6 小时），所以重启不会重复查。第一轮结束后才开始，没有账号能跑第一轮时启动 15 分钟后也开始。
- **手动刷新**：引擎在运行时，命令行和桌面版写一个 `<账号>.subscription` 触发文件并等引擎答复（最多约 75 秒，状态文件只由引擎写）；没有引擎时在本进程里检查并发送告警。
- **提醒**：天数按 UTC 日期计算；状态变化按“已通知的状态”比较，第一次看到非 `Enabled` 的订阅也会提醒；只提醒 `subscription.skus` 关注的订阅，`Deleted` 的订阅只报状态；手动日期只在读不到真实数据时使用。
- **事件内容**：通用 Webhook 的 JSON 和模板变量里有租户（`tenant`）、订阅 ID、SKU、状态和到期日。
- **命令行**：`e5extend subscription` 默认联网刷新，`--cached` 只看保存的结果。
- **权限检查**：令牌里的权限（委托的 `scp`、应用的 `roles`）已知且没有 `Organization.Read.All`（或能读目录的更大权限）时不发请求，直接记为没有权限；没有权限的租户每小时再看一次（补了权限不必等一天）；手动刷新会要一个新令牌，这样刚授予的权限立刻可见。
- **续期的定义**：只有状态为 `Enabled` 且 `nextLifecycleDateTime` 比上次晚才记一次续期；状态变坏时日期会跳到下一阶段，那只是状态变化。
- **到期当天**：除 30 / 7 / 1 天外，到期当天（剩 0 天）和已过期时还会再提醒一次（严重），之后不再重复。
- **补齐权限（修复）**：给账号正在用的应用注册补权限，不新建账号和 Secret，只改该账号自己的应用（找不到或与租户里打了标签的应用不是同一个就拒绝）。桌面版 `AppSetupService.Repair`，命令行 `app setup --app-only --account <名称> --no-new-secret`（委托账号用 `app setup --tenant <租户>`）。
- **时间**：到期日和剩余天数按本机时区的日期计算和显示。
- **分页**：读订阅时跟随 `@odata.nextLink`（最多 10 页）。
