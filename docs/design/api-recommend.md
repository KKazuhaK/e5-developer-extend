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
| 邮箱 | `mail.settings`、`mail.folders`、`mail.messages`、`cal.list`、`contacts.list` | mail、calendar、contacts、tasks、people | HTTP 404、400 | `no_mailbox` |
| OneDrive | `drive.info`、`drive.root` | files、notes、insights | HTTP 404、400 | `no_onedrive` |
| Teams | `teams.joined` | teams、presence | HTTP 404（403 是策略或权限问题，只算 `maybe`） | `no_teams_license` |
| SharePoint | `sites.root` | sites | HTTP 404 | `no_sharepoint` |

- 请求的**路径来自目录**（`API.Path(kind)`），不在代码里再写一遍；全部是 GET，永远不写。
- 探测不会下载大量数据：返回单个对象的 API（`mail.settings`、`drive.info`、`sites.root`）不带查询；`teams.joined` 也不带任何查询
  （Graph 文档说明 List joinedTeams 不支持 OData 查询参数，结果只是用户自己的团队）；其余集合 API 一律 `$top=1&$select=id`。测试检查了这一点。
- “没有”只认 HTTP 404 / 400（Teams、SharePoint 只认 404）；402、403、405、409、422 等其他 4xx 都不能证明“没有”，一律 `maybe` + `probe_failed`。
- 其他分类（user、org、group、apps）没有探测：只要权限够就是 `recommended`。`reports`（需要管理员角色）和 `bookings`（默认关闭，不是每个租户都开了 Bookings，原因码 `optional`）不探测，永远是 `maybe`，只在账号已经选了的时候保留。
- `reports` 没有安全的探测（报表需要管理员角色，请求会返回一个很大的 CSV），权限够时一律 `maybe`，原因 `needs_admin_role`。
- `tasks` 里有 To Do（要 Exchange）也有 Planner（不要）；按 To Do 算，没有邮箱就是 `unavailable`（一半的调用会 404）。
- 应用身份读的是 `/users/{user}/...`：目标用户是账号的 `username`；没填就取租户用户列表的第一个（需要 `User.Read.All`）。
  这个用户是随便挑的，所以应用身份**没填 username** 时，探测得到的“没有”只算 `maybe`，原因码 `probe_arbitrary_user`（只用任意一个用户检查过，填写用户后才可靠），不算 `unavailable`。
  取不到任何用户时，需要目标用户的探测不运行，相关分类 `maybe` + `cannot_probe:user`。

### 探测结果 → 分类状态

| 探测结果（`graph.Class`） | 含义 |
|---|---|
| `ok` | 有 → `recommended` |
| 状态码在“判没有”集合里 | 没有 → `unavailable` + 原因码 |
| 其他（429、5xx、网络错误、401、集合之外的 4xx、超时） | 不确定 → `maybe` + `probe_failed`；**暂时性错误永远不会变成 unavailable** |
| 探测根本跑不了：它的 API 都没有权限（分类本身有权限），或应用身份找不到可看的用户 | `maybe` + `cannot_probe:<缺什么>`：`cannot_probe:scopes:<权限列表>` 或 `cannot_probe:user`。这**不是**暂时失败，不会触发 `--apply` 的拒绝 |

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

`--apply`（要和 `--recommend` 一起用）：把建议的分类写进账号（只改 `categories`，在配置锁里读—改—写），然后 `ReloadEngine`。
**建议的分类 = 推荐的 ∪（`maybe` ∩ 账号现在实际的选择）**。账号现在的选择是它的 `categories`；为空时是目录里默认开启的分类（`Default` 标记，`reports` 不在其中）。
也就是说“可能可用”的分类永远不会因为没能确认而被去掉，不可用的永远不会被留下。
只要有分类的原因是 `probe_failed`（探测真的暂时失败；`cannot_probe` 不算），`--apply` 拒绝写入并提示 “some probes failed temporarily; try again later”，加 `--force` 才写（写入时同样保留 `maybe`）。
“没有要改的”是拿目录的**有效分类**（账号的 `categories`，为空时是目录默认分类）和建议比较，而不是比较原始列表。
**冻结规则**：建议恰好等于目录默认分类时，`categories` 保持**空**（命令行 `--apply` 和桌面版保存都是），这样以后新增的默认分类会自动生效；
不同时才写成显式列表，而显式列表**不会**自动带上以后新增的分类（命令行写完会提示，需要时更新之后再运行一次）。
这个规则成立的前提是目录里每个分类要么全部是默认项、要么全都不是（`internal/catalog` 有测试守着）。
写之前打印改动并确认，`--yes` 跳过确认。建议列表为空时拒绝写入（空列表会让引擎回到“目录默认项”，不是用户想要的）。

## 3. 健康检查

新增每个账号一项 `account.<名称>.apis`（Warn 或 OK）。数据来自调用日志（`store` 里每个调用一行，有 `round`、`api`、`class`、`status`）：

- 只看账号**当前启用**的分类（`categories`，为空时是目录默认分类）：用 `--apply` 关掉的分类，日志里的旧失败不再警告。配置文件没有修改时间可用，所以不按“最近一次改配置之后”过滤，只靠这一条；
- 读最近 14 天该账号的调用行，按 API 在目录里的分类归类，再按轮次（`round`）分组；
- 一个分类在某一轮“失败”＝这一轮里它的调用**全部**是 `not_found`（或 HTTP 400），而且至少有一次；
- 取该分类**最近的 3 轮**（只数出现过该分类调用的轮次，因为每轮只随机抽几个 API），3 轮都失败 → 警告；
- 警告文字：`category mail keeps failing (not found; no mailbox?)`，Fix：`e5extend account test <名称> --recommend`；
- 日志里没有数据、或者没有分类连续失败 → OK。离线模式也运行（只读本地日志）。

GUI 的健康页直接显示 `doctor` 的所有检查，所以不需要改动前端。

## 4. 桌面版向导

步骤顺序改为：**方式 → 详情（自动创建路径：创建应用）→ 登录 → API → 测试 → 完成**。

- 账号在“详情”走到“登录”时保存（和以前“保存之后登录”相同；此时 `categories` 为空）；添加时不再预选全部分类。
  **如实说明**：`categories` 为空意味着引擎用目录默认项。委托账号（设备码、浏览器、密码）登录之前没有令牌，引擎不会调用；
  但 **client_secret 账号**保存后立刻就有令牌，登录之后、选 API 之前，引擎可能已经按默认项跑一轮；
  向导在 API 步骤之前被放弃时，所有账号都会一直按默认项运行（`reports` 不在其中），直到用户改它。
- “API”步骤调用 `AccountService.Recommend(name)`，每个分类卡片带徽标：推荐（绿）、不可用（灰，附原因）、可能可用（黄）。
  新账号（还没有分类）默认勾选推荐的分类，再加上“可能可用”且属于目录默认项的分类；已有分类的账号（编辑）保持当前选择不动，只显示徽标，并在当前选择与推荐不同时提示“建议取消 X、Y”（选了但不可用）和“建议再勾选 X”（推荐但没选）。“全选”改为“按推荐选择”（规则同 `--apply`：推荐的 ∪ 当前已选的“可能可用”，不会因为没确认而去掉），“全不选”保留；有分类因探测暂时失败时显示提示。没有建议可用时（没登录、检查失败），选择为空就勾选目录默认分类（不是全部）。分类列表加载失败时显示错误和“重试”。进入下一步时把所选分类写回账号。
- 没登录（取令牌失败且是 login required）：显示不带徽标的分类，并提示先登录。编辑已有账号时同理：已登录就探测，否则只列分类。
- 探测整体失败（网络等）：不带徽标，提示“检查失败，可稍后在账号详情重试”，并给“重试”按钮。
- 步骤编号变化：登录是第 3 步（原来是第 4 步），所有 `startStep` 调用方（概览和账号页的“重新登录”）改为 3。
- 后端 `AccountService.Recommend` 返回 `{needsLogin, categories[{category,status,reason,default}], recommended[]}`；`CatalogService.DefaultCategories()` 返回目录默认分类，不含任何用户名、令牌。

原因码在界面上翻译（中英文）：`missing_permission:X`（缺少权限 X）、`no_mailbox`、`no_onedrive`、`no_teams_license`、`no_sharepoint`、`optional`、
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
