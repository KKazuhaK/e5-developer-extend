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
| `DelegatedPermissionGrant.ReadWrite.All` | 授予委托权限的管理员同意（`oauth2PermissionGrants`） |
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

- 找到 → **复用**：补齐缺的权限和设置，补齐同意，不新建。
- 没找到，但有显示名是 `e5extend` 或 `E5 Developer Extend` 的应用（手工建的）→ 列出来让用户选“采用它”或“新建”；
  采用时加上标签，并（默认勾选、可取消）改名为规范名称。
- 都没有 → 新建。

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
   有就把 `scope` 补成并集（PATCH），没有就新建（POST）。
6. 把租户 ID 和客户端 ID 填进账号，进入正常登录（用新应用、`.default`）。浏览器里通常已经登录，点一下即可。

**新对象的同步延迟**：刚创建的应用 / 服务主体在几秒到一两分钟内可能查不到。第 4、5 步遇到 404 时退避重试（最多约 2 分钟）；
第 6 步遇到 `AADSTS700016`（找不到应用）同样重试，并在界面上显示“等待微软同步…”。

## 4. 流程（应用身份，Client Secret）

第 1～4 步同上（显示名、标签不同；不设置公共客户端；`requiredResourceAccess` 用应用权限 `appRoles`），然后：

5. 授予应用权限：对每个权限 `POST /servicePrincipals/{Graph}/appRoleAssignedTo`（已存在就跳过）。
6. 创建 Client Secret：`POST /applications/{id}/addPassword`，`displayName = "e5extend <机器名> <日期>"`，有效期 12 个月；
   返回的密钥**直接存进 vault**，不显示、不打印；`secret_expires` 写进配置（到期前会提醒）。
   每台机器各建一个 Secret，所以 Secret 的名字里带机器名，便于以后在后台识别、吊销。
7. 需要 `--username`（读取哪个用户的数据）时照常询问。

## 5. 推荐权限的唯一来源

推荐的委托权限和应用权限从文档里的列表移到代码：`catalog.RecommendedDelegated`、`catalog.RecommendedApplication`。
`docs/graph-api-catalog.md` 和 `docs/user/app-registration.md` 引用同一份列表；加一个测试，保证列表里每个权限都至少被一个 API 用到。

## 6. 失败和权限不足

| 情况 | 处理 |
|---|---|
| 登录的账号不是管理员 / 角色不够（403） | 说明需要的角色：创建应用要“应用程序管理员”或“云应用程序管理员”；授予 Microsoft Graph 应用权限要“特权角色管理员”或“全局管理员”。给出两个选择：换管理员账号重试，或者转到手动模式 |
| 应用已建好，但授予同意失败 | 保留应用，给出管理员同意链接 `https://login.microsoftonline.com/{tenant}/adminconsent?client_id={id}`，让有权限的人点一下；账号照常保存 |
| 租户禁止使用 Graph Command Line Tools（条件访问 / 用户同意被关） | 提示转手动模式，附 `docs/user/app-registration.md` |
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
