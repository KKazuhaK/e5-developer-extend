# 注册 Entra 应用

e5renew 不内置公共 Client ID，需要你在自己的开发者租户里注册一个应用。
入口：[Microsoft Entra 管理中心](https://entra.microsoft.com) → 应用注册 → 新注册。
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
e5renew account add main --auth device_code --tenant <租户 ID 或 organizations> --client-id <客户端 ID>
e5renew account login main
```

浏览器登录用 `--auth browser`。`password`（账号密码）只适用于没开 MFA 的账号，不推荐。

## 二、应用身份（client_secret，不需要用户登录）

1. 新注册，类型选“仅此组织目录中的账户”。
2. 证书和密码 → 新客户端密码 → 复制**值**（只显示一次）。**记下到期日**（最长 24 个月）。
3. API 权限 → 添加权限 → Microsoft Graph → **应用程序权限**，勾选只读权限（见下表）。
4. 点“代表租户授予管理员同意”。应用权限必须经过管理员同意才生效。
5. 添加账号，租户必须是具体的租户 ID 或域名（不能是 `common` / `organizations`）：

```bash
e5renew account add app1 --auth client_secret --tenant <租户 ID> --client-id <客户端 ID> \
        --secret-expires 2027-06-30 --username <目标用户的 UPN 或对象 ID>
```

命令会提示输入密钥。`--secret-expires` 让 e5renew 在到期前 30、7、1 天和到期当天提醒你。
`--username` 是读取哪个用户的邮箱等数据；不指定时，需要授予 `User.Read.All`，程序会从租户里随机挑一个用户。
换新密钥：`e5renew account set-secret app1 --secret-expires <新到期日>`。

## 推荐权限

完整的接口与权限对应关系见 [graph-api-catalog.md](../graph-api-catalog.md)。
权限越多，可调用的接口越多、调用越分散。常用的只读权限：

| 类别 | 委托权限 | 应用权限 |
|---|---|---|
| 用户信息 | `User.Read` | `User.Read.All` |
| 邮件 | `Mail.ReadBasic` 或 `Mail.Read` | `Mail.ReadBasic.All` 或 `Mail.Read` |
| 日历 | `Calendars.Read` | `Calendars.Read` |
| 联系人 | `Contacts.Read` | `Contacts.Read` |
| 文件 | `Files.Read` | `Files.Read.All` |
| 待办 | `Tasks.Read` | `Tasks.Read.All` |

e5renew 请求令牌时使用 `https://graph.microsoft.com/.default`，拿到的是应用注册里**已经同意**的全部权限，
并按这些权限自动筛选可调用的接口。查看当前能用哪些：

```bash
e5renew account test main
e5renew catalog list --account main
```

只授予只读权限。e5renew 的接口库里没有任何写操作。
