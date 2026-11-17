# API 推荐

问题：添加账号的向导先选 API 分类、后登录，而且默认勾选全部分类。没有 Exchange 邮箱的账号（例如没分配 Exchange Online / OneDrive 许可的账号）
照样会去调邮件、日历、联系人、To Do，每一轮都是 404 / 400；完整的 E5 开发者租户账号则全部 200。
分类是否可用取决于账号有没有邮箱、有没有 OneDrive，这只有登录之后才查得出来。

目标：登录之后，用几个很轻的只读请求探测一次，告诉用户每个分类是 **推荐 / 不可用 / 可能可用**，并按推荐预选；
命令行和桌面版共用同一份探测结果；引擎长期运行时，如果某个分类一直 404，在健康检查里提醒。

## 1. 探测：`ops.RecommendCategories`

```go
func (o *Ops) RecommendCategories(ctx context.Context, name string) (Recommendation, error)
```

纯逻辑放在新包 `internal/recommend`（不依赖网络配置、好测试），`ops` 负责取令牌、建 Graph 客户端、调用它：

1. 取令牌（`ops` 里和 `TestAccount` 相同的静默取令牌，带 `WithHint`）。没登录时返回 `auth.ErrLoginRequired`，调用方（GUI）据此显示“请先登录”。
2. 令牌的 `Scopes` 就是已授予的权限（委托是 `scp`，应用是 `roles`）。
3. 用 `catalog.Load()` 得到全部 API，**分类列表和每个分类需要的权限都从目录算出来**，不写死：
   - 分类 = 目录里出现过的 `category`，保持目录顺序；
   - 某分类在这个身份（委托 / 应用）下没有任何带路径的 API → `unavailable`，原因 `not_supported`（例如应用身份没有 OneNote）；
   - `catalog.Available(apis, kind, granted, []string{分类})` 为空 → `unavailable`，原因 `missing_permission:<权限列表>`，
     权限列表取该分类各个 API 的“推荐最小权限”（`scopes` 的第一项），去重；
4. 对通过权限检查的分类，按下面的**探测表**决定要不要探测、探测什么。

### 探测表（唯一一处映射）

| 探测 | 探测的 API（目录里的 id，按顺序取第一个有权限的） | 决定的分类 | 判“没有”的结果 | 原因码 |
|---|---|---|---|---|
| 邮箱 | `mail.settings`、`mail.folders`、`mail.messages`、`cal.list`、`contacts.list` | mail、calendar、contacts、tasks | not_found、client_error（404 / 400） | `no_mailbox` |
| OneDrive | `drive.info`、`drive.root` | files、notes | not_found、client_error | `no_onedrive` |
| Teams | `teams.joined` | teams | not_found、forbidden | `no_teams_license` |
| SharePoint | `sites.root` | sites | not_found | `no_sharepoint` |

- 请求的**路径来自目录**（`API.Path(kind)`），不在代码里再写一遍；全部是 GET，永远不写。
- 其他分类（user、org、group、apps）没有探测：只要权限够就是 `recommended`。
- `reports` 没有安全的探测（报表需要管理员角色，请求会返回一个很大的 CSV），权限够时一律 `maybe`，原因 `needs_admin_role`。
- `tasks` 里有 To Do（要 Exchange）也有 Planner（不要）；按 To Do 算，没有邮箱就是 `unavailable`（一半的调用会 404）。
- 应用身份读的是 `/users/{user}/...`：目标用户是账号的 `username`；没填就取租户用户列表的第一个（需要 `User.Read.All`）。
  这个用户是随便挑的，所以应用身份**没填 username** 时，探测得到的“没有”只算 `maybe`（原因 `probe_failed`），不算 `unavailable`。
  取不到任何用户时，需要目标用户的探测不运行，相关分类 `maybe`。

### 探测结果 → 分类状态

| 探测结果（`graph.Class`） | 含义 |
|---|---|
| `ok` | 有 → `recommended` |
| 在“判没有”集合里 | 没有 → `unavailable` + 原因码 |
| 其他（throttled、server_error、network、auth_failed、canceled、client_error/forbidden 不在集合时） | 不确定 → `maybe` + `probe_failed`；**暂时性错误永远不会变成 unavailable** |
| 没有可用的探测 API（权限不足以探测，但分类本身有权限） | `maybe` + `probe_failed` |

结果类型：

```go
type Recommendation struct {
    Account    string
    Delegated  bool
    Categories []CategoryAdvice   // 目录顺序
}
type CategoryAdvice struct {
    Category string
    Status   string // recommended | unavailable | maybe
    Reason   string // 空，或原因码，missing_permission 带 ":" 和权限列表
}
func (r Recommendation) Recommended() []string // 状态为 recommended 的分类
```

四个探测**并发**执行，总超时 15 秒（`recommendTimeout`）；超时的探测按暂时性错误处理。探测请求之间不需要间隔（只有几个请求，且只在用户操作时发生一次）。

## 2. 命令行

`e5extend account test <名称> --recommend`：先和以前一样打印令牌信息，再打印表格（分类、状态、原因）和一行建议的
`categories = [...]`。

`--apply`（要和 `--recommend` 一起用）：把推荐的分类写进账号（`ops.UpdateAccount`，其他字段不动），然后 `ReloadEngine`。
写之前打印改动并确认，`--yes` 跳过确认。推荐列表为空时拒绝写入（空列表会让引擎回到“目录默认项”，不是用户想要的）。

## 3. 健康检查

新增每个账号一项 `account.<名称>.apis`（Warn 或 OK）。数据来自调用日志（`store` 里每个调用一行，有 `round`、`api`、`class`、`status`）：

- 读最近 14 天该账号的调用行，按 API 在目录里的分类归类，再按轮次（`round`）分组；
- 一个分类在某一轮“失败”＝这一轮里它的调用**全部**是 `not_found`（或 HTTP 400），而且至少有一次；
- 取该分类**最近的 3 轮**（只数出现过该分类调用的轮次，因为每轮只随机抽几个 API），3 轮都失败 → 警告；
- 警告文字：`category mail keeps failing (not found; no mailbox?)`，Fix：`e5extend account test <名称> --recommend`；
- 日志里没有数据、或者没有分类连续失败 → OK。离线模式也运行（只读本地日志）。

GUI 的健康页直接显示 `doctor` 的所有检查，所以不需要改动前端。

## 4. 桌面版向导

步骤顺序改为：**方式 → 详情（自动创建路径：创建应用）→ 登录 → API → 测试 → 完成**。

- 账号在“详情”走到“登录”时保存（和以前“保存之后登录”相同；此时 `categories` 为空，没有令牌之前引擎不会调用任何 API）；
  添加时不再预选全部分类。
- “API”步骤调用 `AccountService.Recommend(name)`，每个分类卡片带徽标：推荐（绿）、不可用（灰，附原因）、可能可用（黄）。
  默认勾选推荐的分类；“全选”改为“按推荐选择”，“全不选”保留。进入下一步时把所选分类写回账号。
- 没登录（取令牌失败且是 login required）：显示不带徽标的分类，并提示先登录。编辑已有账号时同理：已登录就探测，否则只列分类。
- 探测整体失败（网络等）：不带徽标，提示“检查失败，可稍后在账号详情重试”，并给“重试”按钮。
- 步骤编号变化：登录是第 3 步（原来是第 4 步），所有 `startStep` 调用方（概览和账号页的“重新登录”）改为 3。
- 后端 `AccountService.Recommend` 返回 `{needsLogin, categories[{category,status,reason}], recommended[]}`，不含任何用户名、令牌。

原因码在界面上翻译（中英文）：`missing_permission:X`（缺少权限 X）、`no_mailbox`、`no_onedrive`、`no_teams_license`、`no_sharepoint`、
`needs_admin_role`、`not_supported`、`probe_failed`。

## 5. 不做什么

- 不做写入类探测，不读取响应内容（沿用 `graph.Client` 的行为：读完丢弃）；
- 不在引擎运行时自动改账号的分类；健康检查只提醒，是否应用由用户决定；
- 不区分 400 的具体错误码：`graph.Result` 不保留响应体，用状态类别判断已经足够（真实情况是 404 或 400）。

## 6. 测试

- `internal/recommend`：探测表（每个探测的 id 都在目录里、分类都在目录里、分类没有被两个探测抢占）；假 Graph 服务器：完整 E5 用户、
  没邮箱、没 OneDrive、缺权限、暂时性错误（429 / 500 → maybe）、应用身份（有 / 没有 username）。
- `cmd/e5extend`：`--recommend` 的表格输出、`--apply` 写入配置并确认、`--yes`、取消。
- `internal/doctor`：连续 3 轮 404 → 警告；中间有成功 → 不警告；不足 3 轮 → 不警告。
- `gui/services`：`Recommend` 的返回（登录 / 未登录）。
- 前端：原因码到文案的映射、按推荐选择的逻辑（`lib/recommend.ts`）。
