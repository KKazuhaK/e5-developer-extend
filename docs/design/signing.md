# 代码签名（macOS 与 Windows）

本文比较 E5 Renew 的 macOS 和 Windows 发布包可以用的签名方案，给出建议和仓库所有者要亲自做的事。
它接着 [release.md](release.md) 第 7 节“以后再做”里的代码签名一项。

- 资料核对日期：**2026-11-16**。价格、资格和流程都会变，每条结论后面都标了来源；实际注册或付款前请再看一遍原页面。
- 本文只做调研和决策，**没有注册、申请、购买任何东西**，也没有改动工作流。第 5 节写的是以后要改的地方。

## 0. 结论

| 平台 | 建议 | 每年费用 | 签名后的效果 |
|---|---|---|---|
| macOS | Apple Developer Program（**个人**会员）→ Developer ID Application 证书 + 公证（notarization） | **99 美元** | Gatekeeper 不再阻止，只剩所有下载程序都有的“是否打开从互联网下载的应用”确认；没有免费的替代办法 |
| Windows（首选） | **SignPath Foundation**（开源项目免费） | **0** | 显示已验证的发布者“SignPath Foundation”；SmartScreen 仍然可能提示，直到积累信誉。申请表要填“声誉”证明，项目刚发布时很可能不够（见 2.2） |
| Windows（备选） | **Azure Artifact Signing**（原 Trusted Signing），Basic 档 | 约 **120 美元**（9.99 美元/月） | 发布者显示所有者本人的法定姓名；同样要积累信誉。个人只限美国、加拿大居民 |

合计：**每年 99 美元**（SignPath 获批时）或**约 219 美元**（改用 Artifact Signing 时）。

不建议：EV 证书（已经不能跳过 SmartScreen，见 2.3）、
现在就上 Microsoft Store（见 2.5）。
传统 OV 证书（2.3）排在最后：Certum 的开源证书（约 69 欧元）其实比 Artifact Signing 便宜，但私钥只能放在 CA 的云 HSM 里，
CI 要装 SimplySign 虚拟令牌、处理手机动态码，证书每 15 个月左右还要重新签发并重新验证一次，只在前两条都走不通时才用。

所有者要亲自做的事见第 7 节。

## 1. 现状

| 发布物 | 现在怎么做 | 签名情况 |
|---|---|---|
| `E5-Renew_<v>_macos_universal.dmg` | `macos` 任务（`macos-latest`），`scripts/release/macos.sh`：`lipo` 合成通用二进制 → 拼 `.app` → `codesign` → `hdiutil create` | **ad-hoc**（`codesign -s -`）：先签 `Contents/MacOS/e5renew`，再签整个 `.app`；dmg 本身不签 |
| `e5renew_<v>_darwin_universal.tar.gz` | `cli` 任务（Linux），`wails3 tool lipo` | 不签（只有 Go 链接器给 arm64 加的 ad-hoc 签名，合成后是否保留没核实过） |
| `E5-Renew_<v>_windows_{amd64,arm64}_setup.exe` | `windows` 任务（`windows-latest`），`scripts/release/windows.sh`：`go build`（带 `wails3 generate syso` 的资源）→ `makensis` | 不签；NSIS 运行时生成的 `uninstall.exe` 也不签 |
| `e5renew_<v>_windows_{amd64,arm64}.zip` | `cli` 任务（**Linux**）里交叉编译 | 不签 |

已经留好的位置：

- `macos.sh` 读取 `MACOS_SIGN_IDENTITY`。设了真实身份时，会自动加上 `--options runtime --timestamp`（公证要求的 hardened runtime 和安全时间戳），
  签名顺序也已经是“先内层程序、再 `.app`”。缺的是：把证书导入钥匙串、签 dmg、提交公证、staple。
- Windows 没有任何签名位置。安装包里的 `MicrosoftEdgeWebview2Setup.exe` 是微软已签名的文件，不需要也不应该重签。

用户现在看到的：macOS 第一次打开要去“隐私与安全性”里点“仍要打开”；Windows SmartScreen 要点“更多信息 → 仍要运行”。
Windows 11 打开了智能应用控制（Smart App Control）时，未签名且没有信誉的程序会被直接阻止，用户没有“仍要运行”可选（[SmartScreen 信誉][ms-smartscreen]，页面日期 2026-05-04）。

## 2. 各方案

### 2.1 macOS：Developer ID + 公证

**要什么**

- 加入 Apple Developer Program。个人和组织会员都是 **99 美元/年**；免年费只面向非营利组织、教育机构、政府机构，**个人不能申请**（[费用减免][apple-waiver]）。
  - 个人会员：证书和公证信息里显示所有者本人的姓名。注册要用开了双重认证的 Apple 账户，做身份验证。
  - 组织会员：要求法律实体和 D-U-N-S 编号，对一个人维护的项目没有必要。
- Developer ID Application 证书：只有账户持有人（Account Holder）能创建，最多 5 张（[Developer ID][apple-devid]）。
  证书过期或会员到期后，**已经签过、公证过的版本照样能装能用**，只是不能再签新版本；证书被吊销时，已安装的副本也会无法启动。
- 公证：用 `xcrun notarytool submit <dmg> --wait` 提交，成功后 `xcrun stapler staple <dmg>`。要求 Developer ID 签名、hardened runtime、安全时间戳
  （[公证流程][apple-notary]）。`notarytool` 在 GitHub 的 macOS runner 上自带（Xcode 命令行工具）。
- CI 认证用 **App Store Connect API 密钥**（`.p8` + Key ID + Issuer ID），不要用 Apple ID + App 专用密码：
  密钥可以单独吊销，不绑定所有者的个人 Apple 账户。

**有没有免费的办法**：没有。不加入会员就拿不到 Developer ID 证书，也不能公证。ad-hoc 签名（现状）只解决“Apple Silicon 能运行”，不解决 Gatekeeper。
免费的 Apple 账户只能给自己的设备做开发签名，不能分发。

**对本项目的额外好处**：程序把密钥存在 macOS 钥匙串里（`internal/vault`）。钥匙串按代码签名的“指定要求”识别程序，
ad-hoc 签名的指定要求就是这次构建的哈希，每个版本都不同，所以升级后系统可能会再次弹窗询问是否允许访问钥匙串。
有了固定的 Developer ID，升级后身份不变，这类弹窗就不会再出现；但从 ad-hoc 换到 Developer ID 的那一次升级身份本身变了，还会弹一次。
（这是根据钥匙串的工作方式推断的，还没有在真机上验证。）

### 2.2 Windows：SignPath Foundation（开源免费）

SignPath Foundation 用**它自己的证书**替开源项目签名，项目本身拿不到证书（[条款][signpath-terms]，页面未标日期，标注为草稿）。

**资格**（条款原文要求，摘要）：

- 不含恶意软件或可能不需要的程序（PUA）；所有组件都用 OSI 认可的许可证，**没有商业双许可**；不含专有代码。—— MIT、没有闭源部分：**符合**。
- 项目在维护，并且**已经以要签名的形式发布过**。—— v0.1.0 已经发布了 NSIS 安装包：**符合**。
- 下载页上写清楚功能。—— README 和 Release 说明：**符合**。
- 签名团队就是开发团队，拥有源码仓库，只签自己源码构建出的程序。—— **符合**。
- 不能有寻找或利用漏洞、绕过安全措施的功能。—— 本项目是调用 Microsoft Graph 的续期工具，不属于这一类；但“保持 E5 开发者订阅续期”的用途有可能被审核者另眼看待，这一点无法事先确认。
- 隐私和系统改动：未披露的数据传输要有隐私政策并能关闭；改系统配置要明确提示；安装包要能卸载。
  —— 程序只和用户自己配置的 Microsoft Graph 通信、注册计划任务；要在“代码签名政策”里写明。
- 所有团队成员在 SignPath 和代码仓库上都开**多因素认证**。
- 定义角色：作者（Authors）、审阅者（Reviewers，审非提交者的改动）、批准者（Approvers，**每次签名都要人工批准**）。一个人可以兼任全部角色。
- 项目主页要有“Code signing policy”一节，写上规定的致谢语、团队角色和隐私说明。
- 签名的程序要设置并强制产品名称和版本属性（我们已经通过 `wails3 generate syso` 写入了版本资源）。
- 接受技术约束：可验证的构建（只签 GitHub 托管 runner 上构建的产物）、发布前人工批准；配合调查投诉。

**申请表**（<https://signpath.org/apply>，2026-11-16 查看）要填：项目名、仓库地址、主页、下载页地址（**这个页面必须写明项目用 SignPath Foundation 签名**）、
隐私政策地址（收集用户数据时必填）、一句话简介、分类、描述、**声誉（Reputation，必填）**、维护者类型、构建系统、申请人姓名和邮箱，
并勾选同意行为准则、“证书以 SignPath Foundation 的名义签发，违反条款可被吊销”。

**能不能过**：条款没有写最低维护者人数，但表单的“声誉”一栏要求提供“项目被广泛使用或信任”的证据（媒体报道、博客、下载统计、GitHub 数据、社区讨论）。
本项目在 2026-11-16 时仓库刚公开，0 星标，v0.1.0 下载 39 次，**现在申请大概率因为声誉不足被拒**。
更现实的是等有了一定用户（星标、下载量、有人写文章或讨论）再申请。是否接受由基金会自行决定，没有申诉渠道。
项目列表里有 300 多个项目，其中不少是很小的单功能工具（[项目列表][signpath-projects]）；也能看到单人项目在公开的签名政策里写“维护者兼任全部角色”。
所以小项目**有机会，但没有保证**，审批时间也没有公开承诺。

**证书和 SmartScreen**：发布者显示为“SignPath Foundation”，不是所有者本人。证书是 OV 还是 EV，官方页面没有写（第三方说是 OV）；
无论哪种，都不会让 SmartScreen 立刻放行（见 2.3）。好处是这个发布者身份被几百个项目共用，可能已经有一些信誉，但没有官方说法。

**在 GitHub Actions 里怎么跑**（[SignPath GitHub 集成][signpath-gh]）：

- 用 `signpath/github-action-submit-signing-request@v3`：先用 `actions/upload-artifact` 上传未签名的文件，把 `artifact-id` 交给这个 Action；
  SignPath 自己去下载这个 artifact，并核对它来自哪次工作流运行、之前的任务是否都在 GitHub 托管的 runner 上。
- 必需的密钥只有 `SIGNPATH_API_TOKEN`；`organization-id`、`project-slug`、`signing-policy-slug` 不是机密，可以用仓库变量。
- `wait-for-completion` 默认会等签名完成（默认最多 600 秒）；签好的文件下载到 `output-artifact-directory`。
- 每个签名请求都要批准者在 SignPath 网页上点批准。等待时间计入超时，所以要么把超时调长，要么改成“提交后不等待、批准后再运行下一步”。
- **不能在 `makensis` 运行时调用它**：签名是“上传 artifact → 网页批准 → 下载”，不是本地命令。
  所以 NSIS 生成的 `uninstall.exe` 要分两遍构建：先导出卸载程序、签好、再打进安装包（见 [NSIS：外部签名卸载程序][nsis-uninst-ext]）。
  卸载程序是嵌在安装包里的，必须在第二遍 `makensis` 之前签好；它不依赖内层程序，可以和内层程序放在同一个请求里。
  SignPath 的 artifact 配置可以一次签一个 zip 里的多个文件，所以每个版本要批准两次：三个 exe 加卸载程序一次，安装包一次。
- Action 的版本：文档里同时出现 `@v3` 和 `@v1` 的示例，SignPath 的更新日志里有 2.2.0（2026-04）。实施时到该 Action 仓库的 Releases 页面确认实际存在的标签再固定。

### 2.3 Windows：OV / EV 证书

- 2023 年起，所有公开信任的代码签名证书的私钥都必须放在硬件里（USB 令牌或云 HSM），不能再导出成 `.pfx` 放进 CI 密钥。
  要在 GitHub Actions 里用，只能用 CA 的云签名服务（例如 Certum SimplySign、DigiCert KeyLocker、SSL.com eSigner），通常额外收费。
- **有效期**：CA/B 论坛 CSC-31 决议，2026-03-01 起新签发的代码签名证书（OV 和 EV）最长 **460 天**（[DigiCert 说明][digicert-460]；Certum 写的是 459 天）。
  买“三年证书”也要每 15 个月重新签发一次，每次都要重新验证。
- **EV 不再能跳过 SmartScreen**：微软文档明确说，EV 证书过去能直接获得正面信誉，现在已经没有这个效果，
  “只为了避开 SmartScreen 而多付钱买 EV 已经不值得”（[SmartScreen 信誉][ms-smartscreen]）。个人一般也买不到 EV（要求法律实体）。
- 价格（只作量级参考，没有逐家核对）：Certum 面向开源开发者的“Open Source Code Signing（云端）”约 69 欧元，证书主体是“Open Source Developer, 个人姓名”；
  普通 OV 云证书多在每年 100–200 欧元/美元（[Certum 商店][certum-oss]，价格以结账页面为准）。
- 适合的情况：所有者不在美国/加拿大（用不了 Artifact Signing 的个人身份），SignPath 又没批。此时 Certum 的开源证书是最便宜的一条路，
  代价是要装 SimplySign 虚拟令牌，在 CI 里用起来比前两种麻烦。

### 2.4 Windows：Azure Artifact Signing（原 Trusted Signing）

微软托管的签名服务，证书不交给用户，只在签名时由服务使用（[常见问题][ms-as-faq]，页面更新 2026-10-27）。

- **价格**：Basic 档起价 9.99 美元/月（[SmartScreen 信誉][ms-smartscreen]），每月 5000 次签名，每种证书配置文件 1 个；Premium 档每月 10 万次，本项目用不到。
  据常见问题，创建账户就开始计费，按所选档位的整月金额开票、不按天折算（[常见问题][ms-as-faq]）。定价页本身的金额在抓取时显示为占位符（[定价][ms-as-pricing]），具体金额以 Azure 门户为准。
- **订阅**：必须是**付费** Azure 订阅（即用即付即可），免费、试用、赞助订阅都不行（[常见问题][ms-as-faq]）。
- **资格**（[快速入门][ms-as-quickstart]，页面日期 2026-05-21）：
  - 个人开发者：**只限美国和加拿大**。Azure 账单账户必须是“个人”类型，账单上的法定姓名和地址要和政府签发的证件一致；
    验证通过第三方（AU10TIX）+ Microsoft Authenticator 的 Verified ID 完成，要拍证件和人脸。
  - 组织：美国、加拿大、欧盟、英国、澳大利亚、新西兰、日本、韩国、新加坡、瑞士、挪威、以色列。
  - 处理时间 1–20 个工作日。身份验证会过期，要按提醒续。
- **证书内容**：CN 和 O 是验证过的法定姓名，不能自定义；城市、州/省、国家会出现在证书上（街道和邮编可选是否包含）。
  也就是说，**用户在“属性 → 数字签名”里会看到所有者的真实姓名和所在城市**。证书本身有效期很短（几天），服务自动轮换，不需要人管续期。
- **不是 EV**，以后也不会有 EV（[常见问题][ms-as-faq]）。SmartScreen 信誉要随下载量积累，“可能要几周、几百次来自不同用户的干净安装”（[SmartScreen 信誉][ms-smartscreen]）。
  微软文档说用同一个受信任证书签名“可能”让新文件沿用证书的信誉；Artifact Signing 的证书每几天轮换一次，信誉究竟跟着验证过的身份走还是跟着单张证书走，微软没有明确说明，只能实际观察。
- **在 GitHub Actions 里怎么跑**：
  - 用 `azure/login` 走 **OIDC 联合凭据**（工作流需要 `id-token: write`），不需要在 GitHub 里存客户端密钥；
    Entra 应用只给 “Artifact Signing Certificate Profile Signer” 角色。普通联合凭据的 subject 必须完全匹配，不能写 `v*` 这样的通配符，
    所以把 subject 设成 `repo:KKazuhaK/e5-renew:environment:release`，再在 GitHub 上把 `release` environment 限制为只允许 `v*` 标签。
    （Entra 有支持通配的“灵活联合凭据”，但还在预览阶段，不用。）
  - 签名可以用 `azure/artifact-signing-action`（[仓库][ms-as-action]），也可以直接用 Windows SDK 的 `signtool` + Artifact Signing 的 dlib 和一个 `metadata.json`
    （终结点、账户名、证书配置文件名）。`windows-latest` 上有 `signtool`，但**没有** dlib（`Azure.CodeSigning.Dlib.dll`）：
    要从 NuGet 包 `Microsoft.ArtifactSigning.Client`（原 `Microsoft.Trusted.Signing.Client`）装，并且固定用一个够新的 Windows SDK 里的 `signtool`。
  - 因为是本地命令，`makensis` 可以在构建时直接调用它：NSIS 3.08+ 的 `!uninstfinalize` 签卸载程序，`!finalize` 签安装包本身（[NSIS：签名卸载程序][nsis-uninst]）。
  - Linux 上也能签：Jsign 7.0+ 支持 Trusted Signing（`TRUSTEDSIGNING` 存储类型），可以给 `cli` 任务交叉编译出的 Windows 程序签名。不过更简单的做法见第 5 节。

### 2.5 Windows：Microsoft Store / MSIX

- 个人开发者账户**免费**注册（2025 年起，[微软 Store 入门][ms-store]）。
- 从 Store 安装的 **MSIX** 由微软重新签名，**完全没有 SmartScreen 提示**（[SmartScreen 信誉][ms-smartscreen]）。
- 但 Store 收 EXE/MSI 安装包时**不会替你签名**，要求安装包和里面的 PE 文件已经用受信任 CA 的证书签好（[代码签名选项][ms-signing-options]）。
  所以“把现在的 NSIS 安装包上架 Store”不能代替签名，只能在签名之后作为额外渠道。
- 改成 MSIX 和本项目的长期运行设计冲突：MSIX 装在 `C:\Program Files\WindowsApps\<包名>_<版本>_…`，**每个版本路径都不同**，
  而后台服务注册的是固定路径的计划任务（`e5renew service install`），开机启动项也写的是程序路径——这和 release.md 第 2 节不做 AppImage 的理由一样。
  另外 MSIX 有文件系统和注册表虚拟化，`%APPDATA%\e5renew` 等位置的行为要重新验证。
- 结论：**现在不做**。如果以后想让 Store 成为主要渠道，要先单独设计 MSIX 下的后台任务（例如改用包内声明的启动任务），工作量远大于签名本身。

## 3. SmartScreen：签了名也不会马上消失

所有不经过 Store 的方案，第一批用户都**仍然可能看到提示**。区别在于：

- 未签名：提示写“未知发布者”，而且**每个新版本都从零开始**积累信誉。
- 签了名：提示里显示已验证的发布者名字；信誉跟着证书身份走，**之后的版本可以继承**。

E5 Renew 下载量不大，可能要好几个版本才能积累到不再提示。签名后要更新安装说明（`docs/user/install.md`）和 Release 说明里的“未签名”提示：
macOS 那段可以删掉；Windows 那段改成“可能会显示‘已验证的发布者：……’，点‘仍要运行’”。

## 4. 签名能解锁什么

- **Homebrew Cask**：Homebrew 5.0.0（2025-11）宣布官方 `homebrew-cask` 里**无法通过 Gatekeeper 检查的 cask 在 2026 年 9 月禁用**，
  6.0.0（2026-06）重申了这个时间（[Homebrew 5.0.0][brew-5]，[6.0.0][brew-6]）；`--no-quarantine` 选项已经去掉。
  9 月之后的实际执行情况没有找到官方公告，社区报告是已经有 cask 被禁用。
  所以想进官方 cask，**必须先有 Developer ID 签名和公证**；另外官方仓库还有知名度门槛，小项目不一定能进。自建 tap 不受这条审核约束，但用户安装后同样会被 Gatekeeper 拦。
  命令行程序可以走 Homebrew formula（从源码构建，不涉及 Gatekeeper）。
- **winget**：没有找到官方文档要求安装包必须签名，社区仓库里也有未签名的包；但提交时会做 SmartScreen/URL 信誉检查，签名能减少被拦的可能。没有逐条核实，提交前再看 `winget-pkgs` 的最新规则。
- **Scoop**：不要求签名。
- **Wails 应用内更新**：据 Wails v3 文档的搜索摘要（页面本身抓取被拒，没有读到原文），updater 用自己的 Ed25519 密钥验证下载的更新
  （公钥编进程序，`SHA256SUMS` 可作为额外校验，[Wails updater][wails-updater]）。实施前要对照 Wails v3.1.0 的源码确认。
  如果是这样，**这一层和系统代码签名无关**，没有签名也能做。但系统签名仍然有用：
  - macOS：更新替换 `.app` 后，钥匙串弹窗问题同 2.1；新文件如果带隔离属性，ad-hoc 版本会再被 Gatekeeper 拦一次。
  - Windows：程序自己下载的文件一般没有“网络标记”，不触发 SmartScreen；但智能应用控制会检查所有可执行文件，未签名的新版本可能被阻止。
  - 所以 release.md 第 7 节“应用内更新的前提是先有签名”仍然成立，只是要另外准备一对 Ed25519 密钥（又一个发布密钥）。

## 5. 流水线要改什么

原则：**先签内层程序，再打包，再签外层包**；签好以后不能再改文件（改了签名就失效）。只在 `v*` 标签上签名；PR 构建拿不到密钥，继续走现在的 ad-hoc / 不签。
密钥只放进需要它的步骤的 `env`，并且用 GitHub 的 environment（例如 `release`，只允许 `v*` 标签）限制。
注意：如果直接给 `macos`、`windows` 任务写 `environment: release`，PR 上的运行会被 environment 规则**直接拒绝**，现在的 PR 试构建和冒烟测试就跑不了了。
所以要么写成 `environment: ${{ github.ref_type == 'tag' && 'release' || '' }}`，要么把签名放进只在标签上运行的单独任务。

### 5.1 macOS（`macos` 任务、`macos.sh`）

1. 新步骤（只在有密钥时运行）：把 base64 的 `.p12` 解码，导入一个临时钥匙串
   （`security create-keychain` → `unlock-keychain` → `list-keychains -d user -s <临时钥匙串> …`（加进搜索列表，否则 `codesign` 找不到身份）→ `import` → `set-key-partition-list`），
   导出 `MACOS_SIGN_IDENTITY="Developer ID Application: <姓名> (<Team ID>)"`。
2. `macos.sh` 现有逻辑：签 `Contents/MacOS/e5renew` → 签 `.app`（已经带 hardened runtime 和时间戳）。
   需要先在 CI 上确认 hardened runtime 下 WebView 正常：Wails 用的是系统 WKWebView，一般不需要额外 entitlements；如果公证或运行报错，再加 `--entitlements`。
3. 新增：`hdiutil create` 之后 `codesign` 签 dmg 本身。
4. 新增：`xcrun notarytool submit "$dmg" --key AuthKey.p8 --key-id … --issuer … --wait`，失败时 `notarytool log` 打印原因并让任务失败。
5. 新增：`xcrun stapler staple "$dmg"`，再 `stapler validate`。
   只给 dmg 钉票据时，从 dmg 拷出去的 `.app` 本身没有票据，第一次打开要联网让 Gatekeeper 在线查。
   想离线也能打开，就在 `hdiutil create` 之前先把 `.app` 打成 zip 公证一次、`stapler staple` 到 `.app` 上（多一次公证，几分钟）。
6. 冒烟测试加 `spctl -a -t open --context context:primary-signature -vv "$dmg"` 和对 `.app` 的 `spctl -a -vv`，确认 Gatekeeper 接受。
7. **命令行 tar 包**：`darwin_universal` 的 `e5renew` 现在在 Linux 的 `cli` 任务里合成，没法签。建议改到 `macos` 任务里打这个 tar 包：
   和 `.app` 里那份是同一个文件，签名 + 公证一次即可（裸二进制和 tar 包不能 staple，Gatekeeper 会在线查公证票据）。

| 密钥 / 变量 | 内容 |
|---|---|
| `MACOS_CERT_P12`（密钥） | Developer ID Application 证书和私钥，base64 |
| `MACOS_CERT_PASSWORD`（密钥） | `.p12` 的密码 |
| `APPLE_API_KEY_P8`（密钥） | App Store Connect API 密钥（`.p8` 内容），角色选 Developer |
| `APPLE_API_KEY_ID`、`APPLE_API_ISSUER_ID`（变量） | 密钥 ID 和 Issuer ID |
| `MACOS_SIGN_IDENTITY`（变量，可选） | 证书的名字；不设时可以从导入的钥匙串里查 |

### 5.2 Windows，用 Artifact Signing 时（`windows` 任务、`windows.sh`、`installer.nsi`）

1. 任务加 `permissions: id-token: write`，`azure/login` 用 OIDC 登录。
2. `windows.sh` 在 `go build` 之后、`makensis` 之前，用 `signtool sign /fd SHA256 /tr http://timestamp.acs.microsoft.com /td SHA256 /dlib … /dmdf metadata.json`
   签 `e5renew.exe`、`e5renew-bg.exe`、`e5renew-gui.exe`（不签 `MicrosoftEdgeWebview2Setup.exe`）。
3. `windows.sh` 在有凭据时写一个 `sign.cmd` 包装脚本（里面是完整的 `signtool … %1` 命令），只把它的路径用 `/DSIGN_CMD=…` 传给 `makensis`，
   避免 signtool、dlib、metadata 路径经过 bash → `cygpath` → NSIS 多层引号。`installer.nsi` 里用 `!ifdef SIGN_CMD` 包住两行
   （没传时什么都不做）：`!uninstfinalize '"${SIGN_CMD}" "%1"' = 0`（签卸载程序）和 `!finalize '"${SIGN_CMD}" "%1"' = 0`（签安装包）。
   这两个命令要 NSIS 3.08 以上，CI 里要检查 `makensis -VERSION`。
4. **命令行 zip**：现在在 Linux 的 `cli` 任务里交叉编译，没有签名。建议改成在 `windows` 任务里，用第 2 步已经签好的 `e5renew.exe`、`e5renew-bg.exe` 打 zip，
   和安装包里的文件完全相同，只签一次。要改的地方：Git Bash 没有 `zip`，改用 `7z`（runner 自带）或 `Compress-Archive`；
   `cli.sh` 里 `touch -d @SOURCE_DATE_EPOCH` + `zip -X` 的可复现写法要移植过去（签名带时间戳，签过的文件本来就不可逐字节复现，只能保证未签名部分一致）；
   同时从 `cli.sh` 和 `cli` 任务的冒烟测试里去掉 Windows zip，否则 `publish` 用 `merge-multiple` 汇总时会撞名。
   另一个办法是在 `cli` 任务里用 Jsign 签，多一套工具，不值得。
5. 冒烟测试加 `signtool verify /pa /v`，检查安装包、卸载程序和三个 exe。

| 密钥 / 变量 | 内容 |
|---|---|
| `AZURE_CLIENT_ID`、`AZURE_TENANT_ID`、`AZURE_SUBSCRIPTION_ID`（变量） | OIDC 用的 Entra 应用和订阅；不是机密 |
| `ARTIFACT_SIGNING_ENDPOINT`、`ARTIFACT_SIGNING_ACCOUNT`、`ARTIFACT_SIGNING_PROFILE`（变量） | 区域终结点（例如 `https://eus.codesigning.azure.net`）、账户名、证书配置文件名 |

不需要任何机密密钥。

### 5.3 Windows，用 SignPath 时

签名要经过“上传 artifact → 网页批准 → 下载”，所以 `windows` 任务要拆开：

1. `windows-build`：编译三个 exe，导出卸载程序，打成 artifact 上传（不打安装包）。
2. `windows-sign-bin`：`signpath/github-action-submit-signing-request` 提交这个 artifact，等批准，下载签好的文件。
   第一遍 `makensis` 只导出卸载程序，它不依赖内层程序，所以可以在 `windows-build` 里就导出，和三个 exe 放进同一个签名请求。
3. `windows-package`：用签好的 exe 打命令行 zip；第二遍 `makensis` 把签好的卸载程序和 exe 打进安装包。
   不建议跳过卸载程序签名：智能应用控制会检查所有可执行文件，未签名的 `uninstall.exe` 在卸载时可能被阻止。
4. `windows-sign-installer`：提交安装包，等批准，下载；然后冒烟测试、上传给 `publish`。

每次发版要在 SignPath 网页上批准两次。和现在“草稿 Release、所有者手动发布”的流程是一致的，只是标签推送后要守着点批准。
SignPath 那边还要配置项目、artifact 配置（描述 zip 里哪些文件要签）、签名策略。

| 密钥 / 变量 | 内容 |
|---|---|
| `SIGNPATH_API_TOKEN`（密钥） | 有提交权限的 SignPath CI 用户令牌 |
| `SIGNPATH_ORGANIZATION_ID`、项目 slug、签名策略 slug（变量） | SignPath 给的标识 |

### 5.4 这次为什么没改工作流

macOS 要新增导入证书、签 dmg、公证、staple 四步，还要把 darwin 命令行包挪到 `macos` 任务；Windows 的两种方案结构完全不同（一个是本地命令，一个要拆成四个任务）。
在选定 Windows 方案、拿到真实凭据之前加进去的空步骤没法测试，所以这次只写文档。选定后一个 PR 改 macOS，一个 PR 改 Windows，都用一个预发布标签（例如 `v0.1.1-rc.1`）实际跑一遍。

## 6. 风险和注意

- **姓名公开**：Apple 个人会员和 Artifact Signing 个人身份都会把所有者的法定姓名写进签名（Artifact Signing 还有城市和州/省）。SignPath 显示的是基金会名字，没有这个问题。
- **密钥保管**：Developer ID 证书的私钥一旦泄露，Apple 会吊销，**已经发出的版本也会无法启动**。`.p12` 只放 GitHub 密钥，本地留一份离线备份，不要放进仓库或开发机的普通目录。
- **费用中断**：Apple 会员到期不影响已发布版本，只是不能签新版本；Artifact Signing 停付后证书配置文件停用，已签的文件仍然有效。
- **SignPath 的约束**：基金会可以单方面拒绝或撤销；条款还是草稿，以后可能变。要在 README 或项目主页加“代码签名政策”一节。

## 7. 所有者要亲自做的事

### macOS（建议现在做）

1. 在 Apple 账户上开双重认证，按 <https://developer.apple.com/programs/enroll/> 以**个人**身份加入 Apple Developer Program，付 99 美元/年，完成身份验证。
2. 在 Certificates, Identifiers & Profiles 里创建 **Developer ID Application** 证书（要账户持有人本人），在 Mac 上导出成带密码的 `.p12`，离线备份。
3. 在 App Store Connect → 用户和访问 → 集成 → App Store Connect API 里创建一个团队 API 密钥（角色 Developer），下载 `.p8`（只能下载一次），记下 Key ID 和 Issuer ID。
4. 把 5.1 表里的值加到仓库的 `release` environment（限制为 `v*` 标签）。然后通知开发会话改工作流。

### Windows（先试免费的）

1. 在 GitHub 上确认所有协作者都开了双重认证。
2. 在 README（或项目网站）加“Code signing policy”一节：SignPath 要求的致谢语、团队角色（所有者兼任作者、审阅者、批准者）、隐私说明（只连接用户自己配置的 Microsoft Graph，不收集数据）。可以让开发会话起草。
3. 等项目有了可以写进“声誉”一栏的证据（见 2.2）后，在 <https://signpath.org/apply> 提交申请。等结果，期间不需要付任何钱。
   在那之前，Windows 包继续不签名（或者所有者符合条件时先用 Artifact Signing）。
4. 获批后：注册 SignPath 账户并开多因素认证，按基金会给的设置建项目、artifact 配置和签名策略，创建 CI 用户和 API 令牌，填 5.3 表里的值。
5. **如果被拒或者一直没回音**，并且所有者是美国或加拿大居民：
   - 开一个即用即付的 Azure 订阅（账单账户类型为“个人”，姓名地址和证件一致），注册 `Microsoft.CodeSigning` 资源提供程序，建 Basic 档的 Artifact Signing 账户（从这一刻开始按月计费）；
   - 在 Azure 门户完成个人身份验证（手机拍证件 + 人脸，Microsoft Authenticator），等 1–20 个工作日；
   - 建 Public Trust 证书配置文件；建一个 Entra 应用，给它 Certificate Profile Signer 角色，加 GitHub OIDC 联合凭据；填 5.2 表里的值。
6. 两条都走不通时，再考虑 Certum 的开源代码签名证书（2.3）。

## 来源

（核对日期 2026-11-16；括号里是页面自己标的日期，没有的写“未标”。）

- [Apple Developer Program 费用减免](https://developer.apple.com/support/fee-waiver)（未标）
- [Developer ID](https://developer.apple.com/support/developer-id/)（未标）
- [公证流程](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)（未标）
- [SignPath Foundation 条款](https://signpath.org/terms)（未标，草稿）
- [SignPath Foundation 项目列表](https://signpath.org/projects)（未标）
- [SignPath GitHub Actions 集成](https://docs.signpath.io/trusted-build-systems/github)（未标）
- [SmartScreen 信誉](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/smartscreen-reputation)（2026-05-04，更新 2026-08-17）
- [Windows 代码签名选项](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options)（未核对日期）
- [Artifact Signing 常见问题](https://learn.microsoft.com/en-us/azure/artifact-signing/faq)（2026-05-14，更新 2026-10-27）
- [Artifact Signing 快速入门](https://learn.microsoft.com/en-us/azure/artifact-signing/quickstart)（2026-05-21，更新 2026-10-08）
- [Artifact Signing 定价](https://azure.microsoft.com/pricing/details/artifact-signing/)（未标）
- [Artifact Signing 的 GitHub Action](https://github.com/Azure/artifact-signing-action)
- [Microsoft Store 入门](https://learn.microsoft.com/en-us/windows/apps/publish/faq/get-started-with-the-microsoft-store)
- [NSIS：签名卸载程序（3.08+ 的 `!uninstfinalize`）](https://nsis.sourceforge.io/Signing_an_Uninstaller)
- [NSIS：外部签名卸载程序](https://nsis.sourceforge.io/Signing_an_Uninstaller_externally)
- [代码签名证书 460 天有效期（CSC-31，2026-03-01 生效）](https://www.digicert.com/blog/understanding-the-new-code-signing-certificate-validity-change)
- [Certum 开源代码签名（云端）](https://shop.certum.eu/open-source-code-signing-on-simplysign.html)
- [Homebrew 5.0.0](https://brew.sh/2025/11/12/homebrew-5.0.0)（2025-11-12）
- [Homebrew 6.0.0](https://brew.sh/2026/06/11/homebrew-6.0.0/)（2026-06-11）
- [Wails v3 updater（抓取时被拒绝访问，内容来自搜索摘要）](https://v3.wails.io/guides/updater)

[apple-waiver]: https://developer.apple.com/support/fee-waiver
[apple-devid]: https://developer.apple.com/support/developer-id/
[apple-notary]: https://developer.apple.com/documentation/security/customizing-the-notarization-workflow
[signpath-terms]: https://signpath.org/terms
[signpath-projects]: https://signpath.org/projects
[signpath-gh]: https://docs.signpath.io/trusted-build-systems/github
[ms-smartscreen]: https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/smartscreen-reputation
[ms-signing-options]: https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options
[ms-as-faq]: https://learn.microsoft.com/en-us/azure/artifact-signing/faq
[ms-as-quickstart]: https://learn.microsoft.com/en-us/azure/artifact-signing/quickstart
[ms-as-pricing]: https://azure.microsoft.com/pricing/details/artifact-signing/
[ms-as-action]: https://github.com/Azure/artifact-signing-action
[ms-store]: https://learn.microsoft.com/en-us/windows/apps/publish/faq/get-started-with-the-microsoft-store
[nsis-uninst]: https://nsis.sourceforge.io/Signing_an_Uninstaller
[nsis-uninst-ext]: https://nsis.sourceforge.io/Signing_an_Uninstaller_externally
[digicert-460]: https://www.digicert.com/blog/understanding-the-new-code-signing-certificate-validity-change
[certum-oss]: https://shop.certum.eu/open-source-code-signing-on-simplysign.html
[brew-5]: https://brew.sh/2025/11/12/homebrew-5.0.0
[brew-6]: https://brew.sh/2026/06/11/homebrew-6.0.0/
[wails-updater]: https://v3.wails.io/guides/updater
