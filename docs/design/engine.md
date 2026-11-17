# 设计：API 库、调用、状态存储与调度引擎

范围：`internal/catalog`、`internal/graph`、`internal/store`、`internal/engine`，以及 CLI 命令
`run`、`run-now`、`status`、`logs`、`catalog list`、`account pause|resume`。
后台服务安装、IPC、`doctor`、通知和 GUI 不在这次范围内。

本阶段依赖 [auth-and-config.md](auth-and-config.md) 里的 `config`、`vault`、`auth`。

## 0. 与架构文档的差异：不用 SQLite

架构文档原计划用 SQLite 存日志。实际只需要“追加日志 + 按天清理 + 读最近 N 条”，改用：
- 状态：`DataDir()/state.json`（原子写入）
- 调用日志：`DataDir()/logs/calls-YYYY-MM-DD.jsonl`（按天一个文件，只追加，默认保留 30 天）

理由：不引入 `modernc.org/sqlite`（会给二进制加 6–8 MB），而且文本文件方便用户直接查看。
架构文档同步修改。

## 1. API 库 `internal/catalog`

- 数据文件 `internal/catalog/apis.yaml`，用 `go:embed` 嵌入，用 `gopkg.in/yaml.v3` 解析。
- 内容按 [graph-api-catalog.md](../graph-api-catalog.md) 的“只读接口”表逐条录入，**写操作不录入**。

```yaml
- id: mail.messages
  category: mail
  name: Recent messages
  delegated: /me/messages
  app: /users/{user}/messages
  query:                         # 可选；每次调用随机取值
    $top: [5, 10, 20]
    $select: ["subject,receivedDateTime", "id,subject"]
  scopes:
    delegated: [Mail.ReadBasic, Mail.Read, Mail.ReadWrite]
    app: [Mail.ReadBasic.All, Mail.Read, Mail.ReadWrite]
  default: true                  # false 表示默认不启用（例如报表接口要管理员角色）
```

- 方法一律 `GET`。
- `Load()`：解析并校验 id 唯一、路径以 `/` 开头、至少有一种身份、scope 列表非空。
- `Available(apis []API, kind string, granted, categories []string) []API`（包级函数，结果按 id 排序）：
  - `kind` 是 `delegated` 或 `app`；
  - `granted` 和该身份的 scope 列表求交集，不区分大小写；
  - `categories` 为空表示全部，此时只取 `default: true` 的条目；显式列出某个分类时，该分类下的条目全部纳入；
  - 没有对应身份路径的条目（例如 `drive.info` 的应用身份）永远不会被选中。
- `apis.yaml` 里的 any-of 列表除了文档推荐的最小权限，还包含 ReadWrite 等更高权限（它们同样能调用只读接口），
  以及 `Calendars.ReadBasic.All`。`docs/graph-api-catalog.md` 的表格必须和 yaml 完全一致，测试会逐条核对。
- 测试：嵌入的文件能解析；每条 id 唯一；`Available` 的筛选逻辑。

## 2. 调用 `internal/graph`

- `Client{HTTP *http.Client, Base string}`，`Base` 默认 `https://graph.microsoft.com/v1.0`，测试时换成 httptest 地址。
- `ListUserIDs(ctx, token) ([]string, Result)`：取 `/users?$select=id&$top=50` 的用户 ID，只用于应用身份挑选 `{user}`。
  这是唯一会解析响应体的调用，ID 只返回给调用者，不写入日志或状态。
- `Call(ctx, token, path string, query url.Values) Result`：
  - 请求头：`Authorization: Bearer …`、`User-Agent: e5extend/<version>`、`ConsistencyLevel: eventual`（只在需要时加）。
  - 跟随重定向（报表接口返回 302 跳转到 CSV），最多 5 次；**重定向到其他主机时去掉 Authorization 头**（Go 标准库默认就是这样，要有测试）。
  - 响应体最多读 1 MB 后丢弃，**不保存任何响应内容**。
  - 单次请求超时 60 秒。
  - `Retry-After` 无论是秒数还是日期，都限制在 [0, 1 小时] 之内再转换，避免超大值溢出。
- `Result{Status int, Latency time.Duration, Class Class, RetryAfter time.Duration, Err error}`。分类 `Class`：
  - `OK`：2xx
  - `Throttled`：429，或者 503 带 `Retry-After`
  - `AuthFailed`：401
  - `Forbidden`：403（权限不足、没有许可证、功能没开）
  - `NotFound`：404（例如用户没有邮箱）
  - `ServerError`：其他 5xx
  - `Network`：连接、DNS、超时等错误（`ctx` 被取消不算，单独返回 `Canceled`）
  - `ClientError`：其他 4xx

## 3. 状态与日志 `internal/store`

`state.json`（每个账号一项，引擎每轮结束和每次状态变化后原子写入）：

```json
{
  "version": 1,
  "accounts": {
    "main": {
      "next_run": "2026-11-17T09:12:00+08:00",
      "last_run": "…", "last_success": "…",
      "status": "ok | needs_attention | paused | period_ended",
      "reason": "",                       // needs_attention 的原因，带建议
      "consecutive_failed_rounds": 0,
      "granted_scopes": ["Mail.Read", "…"],
      "api_skip_until": { "teams.joined": "2026-11-24T…" },
      "api_forbidden_count": { "teams.joined": 2 },
      "totals": { "rounds": 0, "calls": 0, "ok": 0, "failed": 0 }
    }
  }
}
```

调用日志（每行一个 JSON）：

```json
{"ts":"…","account":"main","round":"<round id>","api":"mail.messages","status":200,"class":"ok","latency_ms":312}
```

另外每轮写一条 `{"ts":…,"account":…,"round":…,"event":"round_end","ok":5,"failed":1,"skipped_reason":""}`。
引擎事件（启动、唤醒检测、暂停原因）也写进同一个文件，带 `event` 字段。

- 接口：`LoadState`/`SaveState`、`Append(entry)`、`Read(filter{account, since, limit}) []Entry`、`Prune(keepDays)`。
- `Append` 写当天的文件（按本地日期），打开时用 `O_APPEND`，单次写入一整行。
- `Prune` 删除超过保留天数的文件；引擎每天调用一次。
- 日志里**不写令牌、密钥、响应内容、用户邮箱**。账号用配置里的名字。

## 4. 调度引擎 `internal/engine`

### 4.1 时钟和主循环

- 注入 `Clock` 接口（`Now()`、`After(d)`），测试用假时钟。
- 主循环每 **30 秒** 醒一次（`tick`，可配置，测试时缩短），每次：
  1. 写心跳文件 `DataDir()/heartbeat`（RFC3339 时间）。
  2. 检测时间跳跃：如果距上次 tick 的墙上时间超过 `3 × tick`，认为发生了睡眠唤醒或改时间。
     这时记录 `resume` 事件，并把所有已到期的 `next_run` 推迟到 `now + 随机 1–3 分钟`，等网络恢复。
     另外，如果墙上时间**往回**跳（`now < 上次 tick − tick`），记录 `clock_change` 事件。
  3. 每 30 次 tick 检查一次配置文件的修改时间，变了就重新加载（另外 Unix 上收到 SIGHUP、或者出现 `trigger/.reload` 文件时立即重载）。
     新配置校验失败时继续用旧配置，并记录错误。
     **`network.proxy` 和 `vault.backend` 的变化需要重启才生效**：重载时如果这两项变了，`config_reload` 事件的说明写
     “restart required for network/vault changes”。
  4. 校正时钟错误：启动时和每次 tick，如果某个账号的 `next_run` 晚于 `now + interval_max + 8 天`，说明保存它的时候时钟偏快、
     现在已被校正（例如 RTC 快了 30 天，NTP 又拨回来）。这时用 `sched.next(now)` 重新安排，并记录 `clock_fix` 事件。
     只有重新安排的时间比原来早 24 小时以上才改，所以因为 `period_start` 在远处而合理地排得很远的 `next_run` 不受影响。
  5. 检查 `DataDir()/trigger/<account>` 文件（`run-now` 创建），有就删掉并立即跑一轮。
  6. 对每个 `next_run <= now`、状态允许运行、并且当前没在跑的账号，启动一轮（每个账号一个 goroutine，同一账号同一时间最多一轮）。
  7. 每天第一次 tick 时调用 `store.Prune`。
- tick、配置重载、结果记账（`finish`）和每一轮都包在 `recover` 里：panic 记录堆栈和一个 `panic` 事件，进程继续运行，锁都用 `defer` 释放。
- 启动时删除 `DataDir()`、`DataDir()/vault` 和配置目录里超过 1 分钟的残留临时文件（`*.tmp-*`、`*.tmp`）。
- **只用墙上时间比较**，不依赖长时间的 `time.Sleep` 或 `time.Timer`（理由见 architecture.md §9.1）。

### 4.2 计算下次运行时间 `nextRun(now, cfg)`

1. `t = now + rand(interval_min, interval_max)`（配置校验要求 `interval_min >= 5m` 且 `interval_max >= interval_min`）
2. 运行期限：`t < period_start` → `t = period_start + jitter`；`t > period_end` → 返回“已结束”。
3. 工作日和工作时段：
   - 如果 `t` 那天不是工作日，或者已经过了 `work_end`，就推到下一个工作日的 `work_start`；
   - 如果早于 `work_start`，就推到当天的 `work_start`；
   - 推过去后再加 `jitter = rand(0, 15 min)`，避免每天同一秒启动。
   - `work_start == work_end` 表示全天。
   - `work_days` 为空表示每天都算工作日。
   - 不支持跨午夜的时段（`work_end < work_start`），配置校验时就拒绝（auth-and-config 阶段已有这条校验的话就复用）。
4. 所有日期计算都用本地时区的日历（`time.Date`），这样夏令时切换当天也是正确的。

**期限结束**：如果下一次运行会落在 `period_end` 之后，但**现在**还没过 `period_end` 那天，账号保持 `ok`，
`next_run` 设为期限结束后的第一刻；tick 看到 `now` 已过期限才把状态改成 `period_ended`。
这样最后一天不会反复“结束又恢复”而连续跑轮。`period_ended` 的账号如果配置里把期限延长了，下一次 tick 自动恢复为 `ok`
（1–5 分钟后开始）。

**所有重试路径**（启动后的 1–5 分钟、睡眠唤醒后的 1–3 分钟、网络问题的 5–15 分钟、需要处理时的 6 小时、
恢复运行后的 1–5 分钟）算出的 `next_run` 也都会被推进工作日和工作时段里，只有手动 `run-now` 不受时段限制。

**启动时**：`state.json` 里的 `next_run` 如果还在未来，就沿用；如果已经过了（关机或睡眠错过了），设为 `now + rand(1, 5 min)`。
**不补跑**错过的轮次。

### 4.3 一轮的执行

1. 取令牌：`auth.Token(ctx)`。
   - 取令牌前，如果上一轮收到过 Graph 的 401，这次强制取新令牌（`auth.ForceRefresh(ctx)`，让 MSAL 忽略缓存的访问令牌）。
   - `ErrTransient`：本轮跳过（记录 `skipped_reason: network`），**不算失败**，`next_run = now + rand(5, 15 min)`。
   - `ErrLoginRequired`、`ErrInvalidClient`、`ErrConsentRequired`：状态设为 `needs_attention`，`reason` 写对应的 CLI 建议。
     之后每 6 小时重试一次（防止误判），重试时令牌取成功就**直接跑完整的一轮**并自动恢复成 `ok`。
   - 其他无法分类的错误也进 `needs_attention`，但状态和日志里只写固定措辞加错误码：
     `sign-in failed (AADSTSnnnn)`、`sign-in failed (HTTP nnn)` 或 `sign-in failed (unknown error)`。
     MSAL/AAD 的原文可能带租户域名、用户名和 correlation id，只写到 stderr 的运行日志。
2. 用令牌里的 scope 更新 `granted_scopes`，再用 `catalog.Available` 得到候选 API。
   排除 `api_skip_until` 里还没到期的条目。候选为空时：`needs_attention`，原因是“没有可用的 API，请检查权限”。
3. 随机抽 `rand(apis_per_round_min, apis_per_round_max)` 个（不超过候选数）。`random_order` 为真就打乱顺序，否则按 id 排序。
4. 应用身份下的 `{user}`：配置里有 `username` 就用它；否则每天第一次需要时调用 `/users?$select=id&$top=50`，从结果里随机选一个并缓存一天。
   这一步失败就跳过需要 `{user}` 的条目；失败（非网络、非限流、非 401）当天不再重试，`run-now` 或手动触发会清掉这个记忆。
   如果一轮下来一次调用都没发出（所选条目全都需要 `{user}`），状态设为 `needs_attention`，
   原因是“set `username` for this account or grant User.Read.All”。
5. 逐个调用，两次调用之间随机等 1–10 秒（可配置，测试时设为 0）。每次调用的结果写日志。
   - `Throttled`：按 `Retry-After` 等待（最多 5 分钟），然后**本轮剩下的调用都不做了**。
   - `Forbidden`：`api_forbidden_count[id]++`；连续 3 次就把该条目加进 `api_skip_until`，跳过 7 天。成功一次就清零。
   - `AuthFailed`：本轮立即停止；下一轮强制取新令牌（见第 1 步）。
   - `Network`：本轮立即停止，**不算失败**，按网络问题处理（同第 1 步）。
6. 一轮结束：
   - 至少一次成功：`last_success = now`，`consecutive_failed_rounds = 0`。
   - 有调用但全部失败（不含网络问题）：`consecutive_failed_rounds++`。达到 3 时自动暂停：状态 `paused`，原因写最近的失败类别。
   - 计算 `next_run`，保存状态。
7. 整轮包在 `recover` 里：panic 记录日志（带堆栈），本轮算失败，下一轮照常。

### 4.4 状态与配置的关系

- `account pause|resume <name>` 修改配置里的 `paused`，引擎重载配置后生效。
- 自动暂停只写在 `state.json` 里。`account resume` 同时清除 `state.json` 里的自动暂停（加锁写回），引擎下一次 tick 读到就恢复运行。
  为此引擎每次 tick 都重新读一次 `state.json` 里由 CLI 改过的字段，用一个 `cleared_by_cli` 标志表示。
  或者更简单：CLI 写一个 `trigger/<account>.resume` 文件，由引擎负责清状态。**采用后者**，状态文件只由引擎写。
- `account pause|resume` 在引擎运行时还会写一个 `trigger/.reload`，让引擎马上重载配置，不用等 30 次 tick。
  `resume` 清除 `paused` 和 `needs_attention`，约 1–5 分钟后重新开始运行。

### 4.5 单实例

- `run` 启动时对 `DataDir()/engine.lock` 加锁（`gofrs/flock`）。因为 `status` 判断引擎是否在运行也是“试着加一下锁”，
  可能刚好占着锁一瞬间，所以最多重试约 3 秒，仍然拿不到才退出，提示“已经有一个引擎在运行”。
- 其他命令通过“试着加一下锁”来判断引擎是否在运行。

### 4.6 关闭

- 收到 SIGINT 或 SIGTERM 时取消 context：正在进行的请求立即中止，保存状态后退出，退出码 0。

### 4.7 日志输出

- 使用 `log/slog`，输出到 stderr（systemd、launchd 和 Windows 服务各自负责收集），用 `--log-format text|json` 选择格式。
- 调用记录走 `store`，不和运行日志混在一起。

## 5. CLI 命令

```
e5extend run [--log-format text|json]     # 前台运行引擎，直到收到信号
e5extend run-now [name...]                # 引擎在运行：写 trigger 文件（30 秒内生效）；
                                          # 引擎没在运行：在当前进程里直接跑一轮并打印结果；
                                          # 配置里暂停的、自动暂停的、期限已结束的账号会被跳过并说明原因
e5extend status                           # 引擎是否在运行、心跳时间；每个账号的状态、下次运行、
                                          # 上次成功、今日成功/失败、原因；last_success 超过 3 天的标红
e5extend logs [name] [--since 24h] [-n 50] [--follow]
e5extend catalog list [--account name]    # 列出 API 库；带 --account 时根据 granted_scopes 显示是否可用
e5extend account pause|resume <name>
```

## 6. 测试

全部用假时钟、httptest 假 Graph 服务器和假的 `Authenticator`，不联网。

- `nextRun`：工作日、工作时段、全天、运行期限、跨周末，以及夏令时切换日（用 `America/New_York`）。
- 启动时 `next_run` 已过期的情况：不补跑，安排在 1–5 分钟后。
- 时间跳跃检测：跳跃后已到期的轮次被推迟。
- 令牌错误：网络错误不计失败；需要重新登录时进入 `needs_attention`，6 小时后重试成功能自动恢复。
- 429 时尊重 `Retry-After` 并结束本轮；403 连续 3 次跳过该 API 7 天，期满后恢复。
- 连续 3 轮全部失败时自动暂停，用 `.resume` 触发文件能恢复。
- panic 被 recover，下一轮照常执行。
- 单实例：第二个引擎拿不到锁。
- `run-now` 的触发文件能被处理。
- 状态在重启后能恢复。
- 日志清理：超过保留天数的文件被删除。
- 跨主机重定向时去掉 Authorization 头。
- **一年模拟**：假时钟连续推进 365 天、2 个账号、默认配置。断言：
  - 每天的轮数在合理范围内，不集中在某个时刻；
  - 只在工作日和工作时段内调用；
  - 日志目录最多 31 个文件；
  - `state.json` 大小不随时间增长；
  - 全程在几秒内跑完。
- 时钟校正：保存的 `next_run` 远在未来（启动时和 tick 时）会被重新安排；往回跳的时钟记录 `clock_change`；
  因 `period_start` 在远处而排得很远的 `next_run` 不被反复改动。
- 期限最后一天的轮数不超过正常上限，之后不再有调用；期限延长后账号自动恢复。
- 应用身份的 `{user}` 查询失败当天只查一次，并进入 `needs_attention`；手动触发会重试。
- 无法分类的令牌错误不会把原文（租户域名、correlation id）写进状态或日志。
- Graph 返回 401 后，下一轮强制取新令牌。
- `run-now`（进程内）跳过自动暂停和期限已结束的账号；重载配置时 proxy/vault 的变化会提示需要重启。
- 引擎锁被短暂占用时 `run` 会等待；结果记账里的 panic 不会让进程退出；启动时清理过期临时文件。
- 一年模拟另外检查：期限内每个工作日每个账号至少有一轮成功；同一账号相邻两轮的间隔不小于 `interval_min`；同一账号的轮次不重叠；
  设置 `period_end` 时最后一天不扎堆、之后停止；时钟先快一个月再拨回来后，一个 `interval_max` 内恢复调度。
