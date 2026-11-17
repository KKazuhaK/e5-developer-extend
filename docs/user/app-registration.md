# 注册 Entra 应用

e5extend 不内置公共 Client ID，需要你在自己的开发者租户里有一个应用注册。

## 推荐：自动创建

用租户管理员账号登录一次，程序在你的租户里建好应用、加好只读权限、授予管理员同意，然后直接进入正常登录。不需要打开 Entra 后台。

**命令行**

```bash
# 创建应用、添加账号并登录（委托身份，设备码或浏览器）
e5extend account add main --auto --auth device_code
# 应用身份（Client Secret）：同时创建一个以本机命名的 Secret，直接存进 vault，不显示
e5extend account add app1 --auto --auth client_secret --username <目标用户的 UPN>
# 只创建/更新应用，打印租户 ID 和客户端 ID
e5extend app setup            # 委托身份
e5extend app setup --app-only --account app1   # 应用身份，Secret 存进 vault 里 app1 的位置
```

`e5extend init` 向导里选“自动创建（推荐）”也是同一个流程。

**桌面版**：添加账号向导第一步选“自动（推荐）”→ 管理员登录（显示设备码或打开浏览器）→ 查看将要创建或更新的内容
（应用名、权限列表；发现已有应用时可选“采用 / 新建”以及是否改名）→ 确认 → 进度 → 进入正常登录。

要点：

- 登录页上显示的是 **Microsoft Graph Command Line Tools**，这是微软的官方工具，只在这一次用来创建应用。
  这次登录的令牌只放在内存里，流程结束就丢弃，不写入 vault 和配置。
- 需要的角色：创建应用要“应用程序管理员”或“云应用程序管理员”；授予 Microsoft Graph 的权限要“特权角色管理员”或“全局管理员”（E5 开发者租户里你自己就是全局管理员）。
- 可以重复执行：已有的应用（按标签 `e5extend` 识别）会被复用，只补齐缺的权限和同意，不会删除任何东西，也不会新建第二个。
  租户里已有手工建的 `e5extend`、`e5renew` 之类同名应用时，会让你选择“采用”（加上标签，默认顺便改成规范名称）或“新建”。
  非交互运行时用 `--adopt <客户端 ID>` 或 `--new` 指定，`--no-rename` 保持原名。
- 刚创建的应用在微软那边要几秒到一两分钟才同步完，程序会显示“等待微软同步…”并自动重试。
- 应用已建好但授予同意失败时，应用会保留，程序给出管理员同意链接
  `https://login.microsoftonline.com/<租户 ID>/adminconsent?client_id=<客户端 ID>`，让有权限的人点一下即可；账号照常保存。
- 如果你的租户禁止使用 Graph Command Line Tools（条件访问或用户同意被关），请用下面的手动步骤。
- 应用身份的 Secret 每台机器各建一个，名字是 `e5extend <机器名> <日期>`，有效期 12 个月，在 Entra 后台的“证书和密码”里可以识别和吊销；重复执行 `--app-only` 会再建一个新的。

规范名称和标签：委托身份 `E5 Developer Extend`（标签 `e5extend`、`e5extend:delegated`），应用身份 `E5 Developer Extend (app-only)`（标签 `e5extend`、`e5extend:app`）。

## 手动注册

不想用自动模式时，在 [Microsoft Entra 管理中心](https://entra.microsoft.com) → 应用注册 → 新注册里自己建。
界面文字可能随微软更新而变化，下面按功能描述。

选一种身份即可（也可以注册两个应用，各用一种）。

## 一、委托身份（以用户身份调用，登录方式 device_code / browser）

1. 新注册：名称随意；“受支持的账户类型”选“仅此组织目录中的账户”或“任何组织目录中的账户”。
2. 身份验证 → 添加平台 → “移动和桌面应用程序”，重定向 URI 填 `http://localhost`。
3. 身份验证 → 高级设置 → **允许公共客户端流** 设为“是”（设备码登录必须）。
4. API 权限 → 添加权限 → Microsoft Graph → **委托的权限**，勾选只读权限（见下表）。
   这些权限大多不需要管理员同意；个别需要时，用租户管理员账号点“代表租户授予管理员同意”。
5. 记下“应用程序(客户端) ID”和租户 ID（或域名）。
6. 添加账号：

```bash
e5extend account add main --auth device_code --tenant <租户 ID 或 organizations> --client-id <客户端 ID>
e5extend account login main
```

浏览器登录用 `--auth browser`。`password`（账号密码）只适用于没开 MFA 的账号，不推荐。

## 二、应用身份（client_secret，不需要用户登录）

1. 新注册，类型选“仅此组织目录中的账户”。
2. 证书和密码 → 新客户端密码 → 复制**值**（只显示一次）。**记下到期日**（最长 24 个月）。
3. API 权限 → 添加权限 → Microsoft Graph → **应用程序权限**，勾选只读权限（见下表）。
4. 点“代表租户授予管理员同意”。应用权限必须经过管理员同意才生效。
5. 添加账号，租户必须是具体的租户 ID 或域名（不能是 `common` / `organizations`）：

```bash
e5extend account add app1 --auth client_secret --tenant <租户 ID> --client-id <客户端 ID> \
        --secret-expires 2027-06-30 --username <目标用户的 UPN 或对象 ID>
```

命令会提示输入密钥。`--secret-expires` 让 e5extend 在到期前 30、7、1 天和到期当天提醒你。
`--username` 是读取哪个用户的邮箱等数据；不指定时，需要授予 `User.Read.All`，程序会从租户里随机挑一个用户。
换新密钥：`e5extend account set-secret app1 --secret-expires <新到期日>`。

## 推荐权限

完整的推荐列表（自动模式使用的同一份）见 [graph-api-catalog.md](../graph-api-catalog.md#推荐的应用注册权限组合)，
接口与权限的对应关系也在那里。
权限越多，可调用的接口越多、调用越分散。常用的只读权限：

| 类别 | 委托权限 | 应用权限 |
|---|---|---|
| 用户信息 | `User.Read` | `User.Read.All` |
| 邮件 | `Mail.ReadBasic` 或 `Mail.Read` | `Mail.ReadBasic.All` 或 `Mail.Read` |
| 日历 | `Calendars.Read` | `Calendars.Read` |
| 联系人 | `Contacts.Read` | `Contacts.Read` |
| 文件 | `Files.Read` | `Files.Read.All` |
| 待办 | `Tasks.Read` | `Tasks.Read.All` |

e5extend 请求令牌时使用 `https://graph.microsoft.com/.default`，拿到的是应用注册里**已经同意**的全部权限，
并按这些权限自动筛选可调用的接口。查看当前能用哪些：

```bash
e5extend account test main
e5extend catalog list --account main
```

只授予只读权限。e5extend 的接口库里没有任何写操作。
