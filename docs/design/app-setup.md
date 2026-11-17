# 自动创建应用注册

目标：添加账号时可以选**自动**，用管理员账号登录一次，程序在用户自己的租户里建好应用注册、加好权限、授予管理员同意，
然后直接进入正常登录；也可以选**手动**，和现在一样自己填租户和客户端 ID。
用户不需要打开 Entra 后台。

## 1. 用什么身份去创建

创建应用需要一个已经存在的客户端去发起管理员登录。用微软官方的公共客户端
**Microsoft Graph Command Line Tools**（`14d82eec-204b-4c2f-b7e8-296a70dab67e`，Graph PowerShell SDK 用的就是它）：

- 每个租户里都能用（多租户、公共客户端，支持设备码和 `http://localhost` 回调）；
- 可以按需申请 Graph 委托权限，管理员在登录页上同意即可；
- 不需要我们自己维护一个多租户应用（那样会显示“未验证的发布者”，而且它失效时所有人都受影响）。

申请的权限（只在这一次登录里用）：

| 权限 | 用途 |
|---|---|
| `Application.ReadWrite.All` | 创建 / 更新应用注册、创建服务主体、为应用身份创建 Client Secret |
| `DelegatedPermissionGrant.ReadWrite.All` | 只有委托身份才申请：授予委托权限的管理员同意（`oauth2PermissionGrants`） |
| `AppRoleAssignment.ReadWrite.All` | 只有应用身份（Client Secret）才申请：授予应用权限 |

这次登录的令牌**只存在内存里**，流程结束就丢弃，不写 vault、不写配置。
界面上提前说明：登录页显示的是 “Microsoft Graph Command Line Tools”，这是微软的官方工具，只用于这一次创建。

## 2. 规范命名和识别

| 身份 | 显示名 | 标签 |
|---|---|---|
| 委托（device_code / browser / password） | `E5 Developer Extend` | `e5extend`、`e5extend:delegated` |
| 应用（client_secret） | `E5 Developer Extend (app-only)` | `e5extend`、`e5extend:app` |

- 应用的 `notes` 写：`Created by E5 Developer Extend <版本> on <日期>. Read-only Microsoft Graph access to keep the tenant active.`
- 一个租户每种身份只有一个应用，多台机器共用，名字里不带机器名。

**可重复执行**：先按标签查（`GET /applications?$filter=tags/any(t:t eq 'e5extend:delegated')`）。
标签查询跑在可能滞后的副本上（刚写入的标签可能查不到），所以**同时**扫描完整的应用列表，在本地按标签识别，两处结果按对象 ID 合并，
列表里带标签的应用不会被丢掉。同一种身份如果有**多于一个**应用带这个标签，直接报错并列出它们的客户端 ID，不替用户挑一个。

- 找到 → **复用**：补齐缺的权限和设置，补齐同意，不新建。更新前先用 `GET /applications/{id}`（不带 `ConsistencyLevel`）
  读一次最新的应用，把缺的内容合并进这份最新数据再 PATCH，不用列表里可能过期的副本，避免把已有的重定向 URI 或其他 API 的权限覆盖掉。
- 没找到，但有显示名是 `e5extend` 或 `E5 Developer Extend` 的应用（手工建的）→ 列出来让用户选“采用它”或“新建”；
  采用时加上标签，并（默认勾选、可取消）改名为规范名称。
- 都没有 → 新建。
- `--adopt X`：已有带标签的应用时，X 必须就是那个应用，否则报错 “an E5 Developer Extend app already exists: <客户端 ID>; drop --adopt”；
  `--adopt` 和 `--new` 同时出现永远报错。

**计划列出每一处改动**：`Plan` 里的动作覆盖 `Apply` 会做的每一次写入——缺的标签（`add_tags`）、改名（`rename_app`）、
`isFallbackPublicClient`（`set_public_client`）、补重定向 URI（`add_redirect_uri`）、备注（`set_notes`）、补权限（`add_permissions`）、
服务主体、同意、Secret。所以“没有动作”就等于 `Apply` 什么都不写，命令行据此跳过确认。测试逐场景对比假服务收到的写入和计划里的动作。

## 3. 流程（委托身份）

1. 管理员登录（设备码或浏览器，租户 `organizations`），从令牌的 `tid` 得到租户 ID，`upn` 显示给用户确认。
2. 读取 Microsoft Graph 的服务主体（`appId = 00000003-0000-0000-c000-000000000000`），
   按**名称**在 `oauth2PermissionScopes` 里查出权限 ID（不在代码里写死 GUID）。
3. 创建或更新应用：
   - `signInAudience = AzureADMyOrg`；
   - `publicClient.redirectUris = ["http://localhost"]`；
   - `isFallbackPublicClient = true`（即“允许公共客户端流”）；
   - `requiredResourceAccess` = 推荐的委托权限（见第 5 节），已有的不删，只补缺的。
4. 创建服务主体（已存在就跳过）。
5. 管理员同意：查 `oauth2PermissionGrants`（`clientId = 本应用服务主体`、`resourceId = Graph 服务主体`、`consentType = AllPrincipals`），
   有就把 `scope` 补成并集（PATCH），没有就新建（POST）。POST 回 409，或 400 “Permission entry already exists” 时说明列表落后了：
   视为已存在，重新读取后补并集。
6. 把租户 ID 和客户端 ID 填进账号，进入正常登录（用新应用、`.default`）。浏览器里通常已经登录，点一下即可。

**新对象的同步延迟**：刚创建的应用 / 服务主体在几秒到一两分钟内可能查不到。第 4、5 步遇到 404 时退避重试（最多约 2 分钟）；
只有请求针对的是**本次运行刚创建**的对象（服务主体、同意、角色分配、Secret）才把 404 当作同步延迟，其他 404 立即报错；
第 6 步遇到 `AADSTS700016`（找不到应用）同样重试，并在界面上显示“等待微软同步…”。

## 4. 流程（应用身份，Client Secret）

第 1～4 步同上（显示名、标签不同；不设置公共客户端；`requiredResourceAccess` 用应用权限 `appRoles`），然后：

5. 授予应用权限：对每个权限 `POST /servicePrincipals/{Graph}/appRoleAssignedTo`（已存在就跳过；POST 回 409 或 400 “already exists” 也算成功）。
6. 创建 Client Secret：`POST /applications/{id}/addPassword`，`displayName = "e5extend <机器名> <日期>"`，有效期 12 个月；
   返回的密钥**直接存进 vault**，不显示、不打印；`secret_expires` 写进配置（到期前会提醒）。
   每台机器各建一个 Secret，所以 Secret 的名字里带机器名，便于以后在后台识别、吊销。
   `secret_expires` 取实际发送的 UTC `endDateTime` 的日期。
   Secret 的名字是 `e5extend <机器名> <账号名> <YYYY-MM-DD>`，同一台机器上两个账号共用一个应用时互不影响。
   **重复执行**：新 Secret 存进 vault 成功**之后**，只删除应用上名字恰好以本机和**本账号**为前缀的旧 Secret（`e5extend <机器名> <账号名> ` 加日期；别的账号、别的机器和手工建的不动），
   结果里列出被删的 key ID。旧格式 `e5extend <机器名> <YYYY-MM-DD>`（不带账号）只有在本机恰好只有一个 client_secret 账号指向该应用、并且就是本账号时才删；
   否则不动，在结果里列为“未删除（有歧义）”（`NotRemovedSecrets`）。
   存储失败则不删旧的，并吊销刚建的新 Secret；吊销也失败时，错误信息写明名为 X、key ID 为 Y 的 Secret 仍然有效、需要到 Entra 里手工删除（不含密钥内容）。
   **写入前检查**：vault 里的 `secret/<账号>` 只在账号还不存在，或账号是 client_secret 且租户 ID、客户端 ID 都和被设置的应用相同时才会被写入；
   否则（例如密码账号、别的应用的账号）在创建 Secret 之前就报错，不留孤立的 Secret。`account add --auto` 在加账号失败时同样会吊销刚建的 Secret 并删除 vault 条目。
7. 需要 `--username`（读取哪个用户的数据）时照常询问。

## 5. 推荐权限的唯一来源

推荐的委托权限和应用权限从文档里的列表移到代码：`catalog.RecommendedDelegated`、`catalog.RecommendedApplication`。
`docs/graph-api-catalog.md` 和 `docs/user/app-registration.md` 引用同一份列表；加一个测试，保证列表里每个权限都至少被一个 API 用到。

## 6. 失败和权限不足

| 情况 | 处理 |
|---|---|
| 登录的账号不是管理员 / 角色不够（403） | 说明需要的角色：创建应用要“应用程序管理员”或“云应用程序管理员”；授予 Microsoft Graph **委托**权限的同意，“云应用程序管理员”或“应用程序管理员”就够（“特权角色管理员”“全局管理员”也行）；授予**应用**权限要“特权角色管理员”或“全局管理员”。给出两个选择：换管理员账号重试，或者转到手动模式 |
| 应用已建好，但授予同意失败 | 保留应用，给出管理员同意链接 `https://login.microsoftonline.com/{tenant}/adminconsent?client_id={id}`，让有权限的人点一下；账号照常保存 |
| 租户禁止使用 Graph Command Line Tools（条件访问 / 用户同意被关；`AADSTS53003`、`65001`、`90094`、`650056/7`、`65002`、`500200`、`50105`、`7000112`、`700016`） | 提示转手动模式，附 `docs/user/app-registration.md` |
| 中途取消 | 已经建好的应用保留（下次自动复用），不留半个账号 |

所有错误信息里不出现令牌和 Secret。

## 7. 入口

**命令行**

```
e5extend app setup [--app-only] [--auth device_code|browser] [--yes]
    # 只创建 / 更新应用，打印租户 ID 和客户端 ID（--app-only 时同时把 Secret 存进 vault 并打印它的名字和到期日，不打印 Secret 本身）
e5extend account add <name> --auto [--auth device_code|browser|client_secret]
    # 先做 app setup，再添加账号并登录
```

`e5extend init` 向导里，“你有没有应用注册？”改成“自动创建（推荐）/ 我已经有了”。

**桌面版**：添加账号向导第一步选“自动（推荐）/ 手动”。自动时：管理员登录（显示设备码或打开浏览器）→ 显示将要创建或更新的内容
（应用名、权限列表，发现已有应用时显示“采用 / 新建”和“改名”选项）→ 确认 → 进度 → 进入正常登录。

## 8. 代码位置

- `internal/auth`：新增一次性登录 `auth.Bootstrap(ctx, method, tenant, clientID, scopes, ui, httpClient) (Token, error)`，内存缓存，不碰 vault。
- `internal/appsetup`：Graph 调用（查找、创建、更新、服务主体、同意、addPassword），全部通过可注入的 HTTP 基址，便于用假服务测试；
  先算出“计划”（要创建 / 更新什么），再执行，方便界面在执行前显示给用户确认。
- `internal/ops/appsetup.go`、`cmd/e5extend/app.go`（或 `appsetup.go`）、`gui/services`、向导页面和中英文文案。
- 文档：`docs/user/app-registration.md` 开头改为“推荐用自动模式”，手动步骤保留。

## 9. 测试

- 假 Graph 服务：全新租户 → 建应用、服务主体、同意；再跑一次 → 什么都不新建；已有应用缺权限 → 只补缺的；
  手工建的 `e5extend` → 采用并改名；同步延迟（前两次 404）→ 重试成功；403 → 给出角色说明；同意失败 → 返回同意链接。
- 应用身份：appRoleAssignedTo 已存在时跳过；Secret 存进 vault、不出现在任何返回值和错误里。
- 推荐权限列表：每个权限都被 API 用到；按名称查不到的权限 → 明确报错而不是悄悄跳过。
