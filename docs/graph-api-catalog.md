# Graph API 库

原则：**默认只读**；每条列出微软文档认可的权限（只要满足其中一个就能调用）；应用身份不支持的接口留空。

**核实情况**：2026-11-16 逐条对照 Microsoft Learn 的 Graph v1.0 参考页（各接口的 Permissions 表）。
写操作一节还没核实。

## 格式（`catalog/apis.yaml`）

```yaml
- id: mail.messages
  category: mail                  # 用户可按分类勾选
  name: 最近邮件
  method: GET
  delegated: /me/messages?$top=10&$select=subject,receivedDateTime
  app: /users/{user}/messages?$top=10&$select=subject,receivedDateTime
  scopes:                         # 满足任意一个即可（any-of）
    delegated: [Mail.ReadBasic, Mail.Read]
    app: [Mail.ReadBasic.All, Mail.Read]
```

权限用 **any-of 列表**，不只写一个“最小权限”。用户的应用注册可能只授了更高的权限（例如 `Mail.Read`），
那样也应该能启用这条接口。引擎从令牌的 `scp` / `roles` 里读出已授权限，和列表求交集即可。

## 只读接口

推荐授予的最小权限写在第一位。后面的 ReadWrite 等更高权限同样能调用只读接口，所以也列在 any-of 里（只用来判断能不能启用，我们不会申请它们）。
本表和 `internal/catalog/apis.yaml` 必须一致，`internal/catalog` 的测试会逐条核对 id、路径和权限集合。

| 分类 | id | 委托路径 | 委托权限（any-of） | 应用路径 | 应用权限（any-of） |
|---|---|---|---|---|---|
| user | user.me | `/me` | User.Read, User.ReadBasic.All, User.Read.All, Directory.Read.All | `/users/{user}` | User.Read.All, Directory.Read.All |
| user | user.list | `/users?$top=20` | User.ReadBasic.All, User.Read.All, Directory.Read.All | `/users?$top=20` | User.Read.All, Directory.Read.All |
| user | user.memberof | `/me/memberOf` | User.Read, GroupMember.Read.All, Directory.Read.All | `/users/{user}/memberOf` | Directory.Read.All |
| user | user.directreports | `/me/directReports` | User.Read + User.ReadBasic.All, User.Read.All, Directory.Read.All | `/users/{user}/directReports` | User.Read.All, Directory.Read.All |
| user | user.settings | `/me/settings`⁷ | User.Read, User.Read.All | — | — |
| org | org.info | `/organization` | User.Read¹, Organization.Read.All, Directory.Read.All | `/organization` | Organization.Read.All, Directory.Read.All |
| org | org.domains | `/domains` | Domain.Read.All, Directory.Read.All | `/domains` | Domain.Read.All, Directory.Read.All |
| group | group.list | `/groups?$top=20` | Group.ReadBasic.All, GroupMember.Read.All, Group.Read.All, Directory.Read.All | `/groups?$top=20` | Group.ReadBasic.All², GroupMember.Read.All, Group.Read.All, Directory.Read.All |
| apps | apps.list | `/applications?$top=10` | Application.Read.All, Directory.Read.All | `/applications?$top=10` | Application.Read.All, Directory.Read.All |
| mail | mail.messages | `/me/messages?$top=10` | Mail.ReadBasic, Mail.Read, Mail.ReadWrite | `/users/{user}/messages?$top=10` | Mail.ReadBasic.All, Mail.Read, Mail.ReadWrite |
| mail | mail.folders | `/me/mailFolders` | Mail.ReadBasic, Mail.Read, Mail.ReadWrite | `/users/{user}/mailFolders` | Mail.ReadBasic.All, Mail.Read, Mail.ReadWrite |
| mail | mail.inbox.delta | `/me/mailFolders/inbox/messages/delta` | Mail.ReadBasic, Mail.Read, Mail.ReadWrite | `/users/{user}/mailFolders/inbox/messages/delta` | Mail.ReadBasic.All, Mail.Read, Mail.ReadWrite |
| mail | mail.rules | `/me/mailFolders/inbox/messageRules` | MailboxSettings.Read, MailboxSettings.ReadWrite | `/users/{user}/mailFolders/inbox/messageRules` | MailboxSettings.Read, MailboxSettings.ReadWrite |
| mail | mail.settings | `/me/mailboxSettings` | MailboxSettings.Read, MailboxSettings.ReadWrite | `/users/{user}/mailboxSettings` | MailboxSettings.Read, MailboxSettings.ReadWrite |
| mail | mail.categories | `/me/outlook/masterCategories` | MailboxSettings.Read, MailboxSettings.ReadWrite | `/users/{user}/outlook/masterCategories` | MailboxSettings.Read, MailboxSettings.ReadWrite |
| calendar | cal.list | `/me/calendars` | Calendars.ReadBasic, Calendars.Read, Calendars.ReadWrite | `/users/{user}/calendars` | Calendars.ReadBasic³, Calendars.ReadBasic.All, Calendars.Read, Calendars.ReadWrite |
| calendar | cal.groups | `/me/calendarGroups` | Calendars.ReadBasic, Calendars.Read, Calendars.ReadWrite | `/users/{user}/calendarGroups` | Calendars.ReadBasic³, Calendars.ReadBasic.All, Calendars.Read, Calendars.ReadWrite |
| calendar | cal.events | `/me/events?$top=10` | Calendars.ReadBasic, Calendars.Read, Calendars.ReadWrite | `/users/{user}/events?$top=10` | Calendars.ReadBasic³, Calendars.ReadBasic.All, Calendars.Read, Calendars.ReadWrite |
| contacts | contacts.list | `/me/contacts?$top=10` | Contacts.Read, Contacts.ReadWrite | `/users/{user}/contacts?$top=10` | Contacts.Read, Contacts.ReadWrite |
| files | drive.info | `/me/drive` | Files.Read, Files.Read.All, Sites.Read.All | —⁴ | — |
| files | drive.list | `/me/drives` | Files.Read, Files.Read.All, Sites.Read.All | — | — |
| files | drive.root | `/me/drive/root/children` | Files.Read, Files.Read.All, Sites.Read.All | `/users/{user}/drive/root/children` | Files.Read.All, Sites.Read.All |
| sites | sites.root | `/sites/root` | Sites.Read.All | `/sites/root` | Sites.Read.All |
| sites | sites.lists | `/sites/root/lists` | Sites.Read.All | `/sites/root/lists` | Sites.Read.All |
| sites | sites.search | `/sites?search=*` | Sites.Read.All | `/sites?search=*` | Sites.Read.All |
| sites | sites.followed | `/me/followedSites` | Sites.Read.All | — | — |
| notes | onenote.notebooks | `/me/onenote/notebooks` | Notes.Create, Notes.Read, Notes.Read.All | —⁵ | — |
| notes | onenote.sections | `/me/onenote/sections?$top=10`⁵ | Notes.Create, Notes.Read, Notes.Read.All | — | — |
| tasks | todo.lists | `/me/todo/lists` | Tasks.Read, Tasks.ReadWrite | `/users/{user}/todo/lists` | Tasks.Read.All, Tasks.ReadWrite.All |
| tasks | planner.tasks | `/me/planner/tasks` | Tasks.Read, Group.Read.All | `/users/{user}/planner/tasks` | Tasks.Read.All |
| tasks | planner.plans | `/me/planner/plans` | Tasks.Read, Group.Read.All | — | — |
| teams | teams.joined | `/me/joinedTeams` | Team.ReadBasic.All, TeamSettings.Read.All | `/users/{user}/joinedTeams` | Team.ReadBasic.All, TeamSettings.Read.All |
| insights | insights.trending | `/me/insights/trending?$top=10`⁸ | Sites.Read.All | — | — |
| people | people.list | `/me/people?$top=10`⁹ | People.Read, People.Read.All | — | — |
| presence | presence.me | `/me/presence` | Presence.Read, Presence.Read.All | `/users/{user}/presence` | Presence.Read.All |
| bookings | bookings.businesses | `/solutions/bookingBusinesses`¹⁰ | Bookings.Read.All, Bookings.ReadWrite.All, Bookings.Manage.All | `/solutions/bookingBusinesses` | Bookings.Read.All, Bookings.ReadWrite.All, Bookings.Manage.All |
| bookings | bookings.currencies | `/solutions/bookingCurrencies`¹⁰ | Bookings.Read.All, Bookings.ReadWrite.All, Bookings.Manage.All | — | — |
| reports | reports.active | `/reports/getOffice365ActiveUserDetail(period='D7')`⁶ | Reports.Read.All（还需管理员角色） | 同左 | Reports.Read.All |

`{user}`：应用身份下的目标用户 ID 或 UPN。来自配置，或者启动时从 `/users` 随机取一个。

### 脚注

1. 只有 `User.Read` 时，`/organization` 只返回 `id`、`displayName`、`verifiedDomains`，但调用本身是成功的。
2. `/groups` 文档列出的“最小权限”是 `Group-NestingSupport.ReadWrite.All`，这在语义上并不是更低的权限，看起来是文档的问题。
   我们按实际权限高低排序，`Group.ReadBasic.All` 也在文档认可的列表里。
3. 日历页面列出的应用权限名是 `Calendars.ReadBasic`，不是 `Calendars.ReadBasic.All`。实测时要确认令牌 `roles` 里实际出现的是哪个名字。
4. `GET /users/{id}/drive` 的文档写的是**应用权限不支持**，所以应用身份只用 `drive.root`。实测时再确认。
5. OneNote 的**应用身份已于 2025-03-31 停用**（Message Center MC1011142）。参考页的表格里还列着 `Notes.Read.All`，但实际已经调不通。
6. 报表接口返回 302 跳转到 CSV，客户端要跟随跳转。委托身份调用时，用户还必须有 Entra 的报表相关管理员角色。
7. `GET /me/settings`（v1.0，用户设置）只支持委托身份。原版的“区域和语言设置”（`/me/settings/regionalAndLanguageSettings`）只在 beta 里，我们只用 v1.0，所以没有收。
8. `/me/insights/trending` 只支持委托身份；权限用已有的 `Sites.Read.All`，数据来自 SharePoint 和 OneDrive 上的文档，所以 API 推荐按 OneDrive 的探测结果判断 `insights` 分类。
9. People API 处于维护模式但仍可调用；只有委托身份，数据来自邮箱，所以 API 推荐按邮箱的探测结果判断 `people` 分类。
10. Bookings 默认关闭（不是每个租户都开了 Bookings），列表接口不支持 `$top`。需要 `Bookings.Read.All`。`bookingCurrencies` 的文档没有应用权限，所以只有委托路径。

## 从初稿删掉或降级的条目

| 条目 | 处理 | 原因 |
|---|---|---|
| `insights.used`、`insights.shared`（`/me/insights/used`、`/shared`） | **不收** | 已弃用：2026 年 11 月之后不再返回数据 |
| `drive.recent`（`/me/drive/recent`）、`drive.sharedWithMe` | **删除** | 已弃用：降级运行到 2026 年 11 月，之后不再返回数据，也就是现在 |
| `user.people`（`/me/people`） | **重新加入**为 `people.list`（`people` 分类） | People API 进入维护模式，但还能调用；原版也在用 |
| `/me/manager` | 不收 | 没有上级的账号（开发者租户里常见）会返回 404，每轮都显示为失败；只收 `user.directreports`（空列表也是 200） |
| 某个团队的频道列表 | 不收 | 需要先取团队 ID 再请求第二步，引擎目前一次只发一条独立的请求 |
| OneNote 应用身份 | 删除应用路径 | 见脚注 5 |
| `drive.info` 应用身份 | 删除应用路径 | 见脚注 4 |
| `planner.tasks` 应用身份 | **补上** | 初稿以为不支持，文档实际支持 `Tasks.Read.All` |
| `user.memberof` 应用权限 | 改为 `Directory.Read.All` | 文档对应用身份没有列 `GroupMember.Read.All` |

## 可选的写操作（默认关闭，**未核实**）

都是“建了马上删”，不留痕迹。实现前还要逐条核对文档：

| id | 操作 | 预计权限 |
|---|---|---|
| write.drive.tempfile | 在 OneDrive 的应用文件夹里上传一个小文件，再删除 | Files.ReadWrite.AppFolder |
| write.mail.draft | 建一封草稿，再删除 | Mail.ReadWrite |
| write.todo.task | 在专用列表里建一个任务，再删除 | Tasks.ReadWrite |
| write.cal.event | 建一个私有事件，再删除 | Calendars.ReadWrite |

## 推荐的应用注册权限组合

推荐权限的**唯一来源**是代码里的两个列表：`catalog.RecommendedDelegated`（委托身份，设备码或浏览器登录）和
`catalog.RecommendedApplication`（应用身份，Client Secret），见 [internal/catalog/recommended.go](../internal/catalog/recommended.go)。
自动创建应用（`e5extend app setup`、桌面版的添加账号向导）直接使用这两个列表，并按名称向 Microsoft Graph 查出权限 ID。
手动注册时照着这两个列表勾选；`e5extend app setup` 在执行前也会把完整列表打印出来。

- 委托身份：尽量选普通用户能自己同意的低权限。`GroupMember.Read.All` 需要管理员同意（E5 开发者租户里你自己就是管理员，所以不是问题）。
  其余是否需要管理员同意，以 Entra 后台“API 权限”页显示为准，实测时核对。`offline_access` 不对应任何接口，只是让登录可以续期。
- 应用身份：全部需要管理员同意。
- 新增的三个权限都是只读：`People.Read`、`Presence.Read`、`Bookings.Read.All`（委托），`Presence.Read.All`、`Bookings.Read.All`（应用）。自动创建应用会一并申请；已有的应用注册没有这些权限时，对应接口只是不会被选中，不影响其他接口。
- `Organization.Read.All` 在两个列表里都有：除了“组织信息”接口，订阅到期检测（`GET /directory/subscriptions`，见 [docs/design/subscription.md](design/subscription.md)）也读它；委托身份下它需要管理员同意。
- 测试保证列表里的每个权限（`offline_access` 除外）都至少被 `apis.yaml` 里的一个接口用到，所以列表不会悄悄变成多余的权限。

## 还要实测确认的事

- [ ] 脚注 3：应用身份下日历权限在令牌里的实际名字
- [ ] 脚注 4：`/users/{id}/drive` 用应用身份是否真的被拒
- [ ] `sites.search` 的 `search=*` 在 E5 开发者租户里能否返回结果
- [ ] 报表接口在开发者租户里是否有数据（新租户可能要等 48 小时）
