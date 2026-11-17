# 让它一直在后台跑

`e5extend service install` 在当前用户下注册一个后台任务：登录后自动启动，崩溃后自动重启。

```text
e5extend service install [--key-file] [--enable-linger] [--headless]
e5extend service uninstall
e5extend service status
```

加 `--print` 只显示将要写入的文件和执行的命令，不做任何修改。

可执行文件必须在固定位置：`go run` 的构建目录、`/tmp` 等临时目录会被拒绝。
安装后不要移动或删除这个文件；升级时覆盖同一路径，再重启服务。

## Linux 桌面

```bash
e5extend service install
e5extend service status
e5extend doctor
```

写入 `~/.config/systemd/user/e5extend.service`（`Type=notify`、`Restart=on-failure`、`WatchdogSec=120`），
然后执行 `systemctl --user daemon-reload` 和 `systemctl --user enable --now e5extend.service`。
重新安装时还会执行 `systemctl --user restart e5extend.service`，让新的 unit 生效。
引擎卡死时，systemd 的看门狗会自动重启它。unit 里没有 `network-online.target`（用户级 systemd 里没有这个 target），
断网时引擎自己重试。

登录桌面后服务可以访问系统钥匙串，不需要 `--key-file`。

## Linux 服务器（无桌面）

服务器上有两件事要处理：

1. **linger**：默认情况下用户退出登录后，用户服务会被停掉。需要执行 `loginctl enable-linger $USER`。
   `service install --enable-linger` 会替你执行；失败时会打印需要 sudo 的命令。不开的话安装结果里会有醒目的警告，
   `doctor` 的 `linger` 项也会报警。
2. **主密钥**：systemd 用户服务通常访问不到钥匙串（Secret Service）。用 `--key-file`：

```bash
e5extend service install --key-file --enable-linger
```

`--key-file` 把当前主密钥写到 `<配置目录>/master.key`（权限 0600），并在 unit 里用 `LoadCredential=` 交给服务。
`LoadCredential=` 在用户服务里的支持：systemd NEWS 里最早明确提到用户服务可以用凭据的是 v258（加密凭据）；
在 systemd 259 上实测 `systemd-run --user --property=LoadCredential=...` 可用，`$CREDENTIALS_DIRECTORY` 里的文件权限为 0400。
安装时 `systemctl --version` 低于 258 的话，改为在 unit 里写 `Environment=E5EXTEND_MASTER_KEY_FILE=<配置目录>/master.key`，
程序直接读这个文件（同样只靠文件权限保护），并给出提示。`--print` 不查询 systemd，总是显示 `LoadCredential=` 的形式。
如果向导里的主密钥来自环境变量，`init` 会自动为服务加上 `--key-file`。
**取舍**：密钥文件只靠文件权限保护，能读到你家目录的人（包括 root）就能解开 vault。
如果 `vault.backend` 是 `keyring`，key 文件不会被读取，需要先改成 `auto` 或 `file`。
shell 里设置的 `E5EXTEND_MASTER_KEY` 不会传给服务，同样要用 `--key-file`。

卸载（`service uninstall`）不会删除 `master.key`，因为它可能是 vault 密钥的唯一副本；不需要时请自己删除。

验证：

```bash
e5extend service status      # installed / enabled / running / linger 都应该是 yes
systemctl --user status e5extend
journalctl --user -u e5extend -f
e5extend doctor
```

SSH 登录时如果报 `Failed to connect to bus`，执行 `export XDG_RUNTIME_DIR=/run/user/$(id -u)` 后重试。
系统级服务（`--system`）暂未实现。

## macOS

```bash
e5extend service install
```

写入 `~/Library/LaunchAgents/io.github.kkazuhak.e5extend.plist`（`RunAtLoad`、`KeepAlive`、`ThrottleInterval 30`，
`ProcessType` 为 `Standard`），然后依次执行 `launchctl enable gui/<uid>/io.github.kkazuhak.e5extend` 和
`launchctl bootstrap gui/<uid> <plist>`（被禁用的 label 不能 bootstrap，所以先 enable）。
已经装过时会先 `launchctl bootout`，稍等后再 bootstrap；bootstrap 失败会再试一次（旧进程可能还没退出）。
运行日志（带轮转）在 `~/Library/Application Support/e5extend/engine.log`（`run --log-file`）；
`~/Library/Logs/e5extend/engine.log` 只接收启动早期的崩溃输出。

要在 Mac 本机的终端里执行（需要已登录的桌面会话）；通过 SSH 执行可能会失败。
登录会话里可以访问钥匙串，不需要 `--key-file`。合上盖子睡眠时程序不会运行。

验证：`e5extend service status`、`launchctl print gui/$(id -u)/io.github.kkazuhak.e5extend`、`e5extend doctor`。

## Windows

```powershell
e5extend service install
```

注册一个名为 `e5extend` 的**计划任务**：当前用户登录时运行，没有 72 小时的执行时限，使用电池时也不停止，
失败后每分钟重启一次（最多 999 次）。另外每 5 分钟（无限期）触发一次，已在运行时忽略（`IgnoreNew`，引擎自己的单实例锁也会拒绝重复启动），
所以进程崩溃后最迟 5 分钟内会被重新拉起。重新安装时先结束旧实例再启动。

- 优先使用同目录下的 `e5extend-bg.exe`（窗口子系统版本，不弹控制台窗口）。找不到时会改用 `e5extend.exe`，
  并警告每次启动都会出现一个控制台窗口。请把两个文件放在同一目录（安装包和 zip 包里两者都有）。自己从源码构建时：
  `GOOS=windows go build -ldflags -H=windowsgui -o e5extend-bg.exe ./cmd/e5extend`（在 Windows 上用 PowerShell 设置 `$env:GOOS` 可省略）。
- 任务没有控制台，运行日志写到 `%LOCALAPPDATA%\e5extend\engine.log`（超过约 5 MB 时转存为 `engine.log.1`）；程序崩溃时的堆栈另外写到同目录的 `engine-crash.log`。
- 任务定义的 XML 保存在 `%LOCALAPPDATA%\e5extend\service\e5extend-task.xml`。
- 任务以你自己的身份运行，可以读取凭据管理器里的主密钥。`--key-file` 不适用于 Windows。
- **默认的任务只在你登录期间运行**：注销后会停止，重启后没人登录就不会启动。需要无人登录也运行（Windows Server、Server Core、无人值守的机器）时，用下面的 `--headless`。
- 计划任务没法设置环境变量。如果你在安装时用了 `E5EXTEND_CONFIG_DIR` / `E5EXTEND_DATA_DIR`，安装程序会把这两个目录改成 `--config-dir` / `--data-dir` 参数写进任务，效果相同。

验证：`e5extend service status`、`schtasks /Query /TN e5extend`、`e5extend doctor`。
Windows 服务（LocalSystem）形式暂未实现。

### Windows：无人登录也运行（`--headless`）

```powershell
# 在“以管理员身份运行”的 PowerShell 或命令提示符里，用同一个用户：
e5extend service install --headless
e5extend service status
```

`--headless` 让计划任务像“不管用户是否登录都要运行”那样注册：

- 任务主体的登录类型是 **S4U**（Service-for-User）：以当前用户的身份运行，**不需要、也不保存 Windows 密码**。除原有的登录触发器和每 5 分钟重复的触发器外，再加一个**开机触发器**（开机后延迟 30 秒启动，网络没好时引擎自己会重试）。失败重启、电池、`IgnoreNew` 等设置不变，仍是 `LeastPrivilege`（不提升权限）。
- **主密钥文件**：S4U 会话没有密码，Windows 不会为它加载你的 DPAPI 用户主密钥，也看不到你的凭据管理器，所以任务读不到默认的主密钥。`--headless` 把当前主密钥复制成 `%APPDATA%\e5extend\master.key.dpapi`（配置目录里），内容用 **DPAPI 本机范围**（`CryptProtectData` + `CRYPTPROTECT_LOCAL_MACHINE`）加密，任务通过 `--dpapi-key-file <路径>` 参数读取（任务动作不能设环境变量）。程序不会打印这个密钥。
- **文件权限（ACL）**：本机范围的 DPAPI 数据，本机任何账户的进程都能解密，所以真正的保护是 NTFS 权限：文件的 ACL 只授予当前用户、SYSTEM 和 Administrators，并且去掉了继承自文件夹的权限项。管理员本来就能读到这个文件（和任何本机范围的 DPAPI 数据一样）。文件创建时就带着这个 ACL（用 `CreateFile` 的安全属性，不是先创建再改权限），并且不共享，写完才改名到位。配置目录如果是 `--headless` 新建的，也只授予这三者；已经存在的目录不会改动，但如果别的账户能在里面写入、删除或改权限（他们能替换密钥文件），`doctor` 会警告并写出目录。文件被复制到别的机器上无法解密。
- 这个文件只是主密钥的**一份副本**：原来的密钥（凭据管理器，或你保存的 `E5EXTEND_MASTER_KEY`）不受影响。你之后换了主密钥的话，重新执行 `service install --headless`。

#### 前提和限制

- **需要“作为批处理作业登录”权限（`SeBatchLogonRight`）**：S4U 任务需要它。Administrators 组默认就有；在“本地安全策略 > 本地策略 > 用户权利指派”里，“作为批处理作业登录”被收紧、或者“拒绝作为批处理作业登录”包含你的账户时，注册或启动会失败（错误里会提到登录类型或 1385）。
- **注册任务通常需要管理员权限**（开机触发器、S4U 都是）。`service install --headless` 失败并提示“拒绝访问”或当前不是提升的进程时，会打印要在管理员命令行里执行的完整命令。务必用**同一个用户**打开管理员命令行：换成另一个管理员账户的话，任务会以那个账户注册，读写的也是它的配置目录。
- **S4U 会话访问不到需要你 Windows 凭据的网络资源**（文件共享、需要 Kerberos 委派的服务）。e5extend 只通过 HTTPS 访问 Microsoft Graph（以及你配置的通知渠道），这不受影响；但如果你的数据目录或代理需要网络凭据，就不能这样用。
- 引擎的数据目录和配置目录要放在本机的普通目录里（默认的 `%APPDATA%` 和 `%LOCALAPPDATA%` 就可以）。
- `vault.backend = "keyring"` 时不会读主密钥文件，需要先改成 `auto` 或 `file`。
- `--headless` 只用于 Windows；在 Linux 上请用 `--key-file` 和 `--enable-linger`，macOS 没有对应的功能。

#### Server Core（没有桌面的 Windows Server）

1. 用命令行版的 zip（`e5extend_<版本>_windows_amd64.zip`，不是安装程序，也不是桌面版），解压到固定的位置，例如 `C:\Program Files\e5extend`，`e5extend.exe` 和 `e5extend-bg.exe` 放在一起。
2. 在管理员命令行里（本机控制台、RDP 或 SSH 都可以）：

   ```powershell
   e5extend init
   e5extend service install --headless
   e5extend service status
   ```

   通过 SSH 登录时凭据管理器通常不可用：先用 `e5extend vault init --print-key` 生成密钥并**另外保存好**，在这个 shell 里设置 `$env:E5EXTEND_MASTER_KEY = "<密钥>"`，再执行 `init` 和 `service install --headless`；任务读取的是 `master.key.dpapi`，不依赖这个环境变量。
3. 验证：`e5extend service status` 里应有 `installed: yes`、`headless: yes`，且 `engine: running`；`e5extend doctor` 的“Headless mode”项为 ok。重启机器后不登录，等一两分钟再看一次。运行日志在 `%LOCALAPPDATA%\e5extend\engine.log`。

`service status` 和 `doctor` 会报告：任务是不是 headless、主密钥文件是否存在并能解密（读不到时引擎无法打开保险库，`doctor` 标为失败）、文件的 ACL 是否比“用户 + SYSTEM + Administrators”更宽（警告；重新执行 `service install --headless` 会重置）。

在 headless 和普通任务之间切换：普通的 `service install` 覆盖 headless 任务时会删除不再使用的 `master.key.dpapi`；`service uninstall` 只要配置目录里有这个文件（它只会由 `--headless` 创建）就删除，不管任务当前是什么类型。任务注册成功但启动失败时，密钥文件保留，并提示用 `schtasks /Run /TN e5extend` 启动。`doctor` 还会用密钥文件里的密钥解密保险库里的一项数据，密钥换过之后文件变旧会报错，重新执行 `service install --headless` 即可。

卸载（`service uninstall`）会删除任务；**只有任务是用 `--headless` 装的**才会同时删除 `master.key.dpapi`，并说明主密钥本身、保险库和配置都没有动。普通任务的卸载不碰任何密钥文件。

## 桌面应用（托盘）

桌面应用的托盘模式在没有后台服务时，会在托盘进程里运行引擎。**一旦安装了后台服务，托盘就只监视**：它显示状态，但不运行引擎，也不会去抢引擎的锁。
如果服务已安装但引擎没有在运行，托盘图标会变成警告，菜单里有“检查问题…”可以打开健康检查页面；修好服务（`e5extend service status`、`e5extend doctor`）后警告会自行消失。

## Docker

见 [docker.md](docker.md)。验证：`docker compose logs -f`、`docker compose exec` 不可用（镜像没有 shell），
可以用 `docker compose run --rm e5extend status` 查看状态。
