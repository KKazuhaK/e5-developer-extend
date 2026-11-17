# 安装

从 [GitHub Releases](https://github.com/KKazuhaK/e5-developer-extend/releases) 下载对应的文件。
每个版本都附带 `SHA256SUMS`，下载后可以校验：

```bash
sha256sum --ignore-missing -c SHA256SUMS      # Linux
shasum -a 256 --ignore-missing -c SHA256SUMS  # macOS
```

**第一版没有代码签名**，macOS 和 Windows 第一次打开时会拦一下，下面各节写了怎么放行。

## 选哪个文件

| 系统 | 桌面版（托盘 + 窗口） | 只要命令行（服务器） |
|---|---|---|
| Windows 10/11 | `E5-Developer-Extend_<版本>_windows_amd64_setup.exe`（ARM 电脑选 `arm64`） | `e5extend_<版本>_windows_amd64.zip` |
| macOS 12+ | `E5-Developer-Extend_<版本>_macos_universal.dmg`（Intel 和 Apple 芯片通用） | `e5extend_<版本>_darwin_universal.tar.gz` |
| Ubuntu 24.04+ / Debian 13+ | `e5extend_…deb` **和** `e5extend-desktop_…deb` | `e5extend_…deb` |
| Fedora 39+ | `e5extend-…rpm` **和** `e5extend-desktop-…rpm` | `e5extend-…rpm` |
| 其他 Linux | 暂无（见下文） | `e5extend_<版本>_linux_amd64.tar.gz` / `arm64` |
| Docker | — | `ghcr.io/kkazuhak/e5-developer-extend`，见 [docker.md](docker.md) |

桌面版里已经带了命令行程序：后台服务实际运行的是命令行程序，所以两者必须放在一起，安装包会自动处理。

## Windows

1. 运行 `E5-Developer-Extend_…_setup.exe`。如果出现“Windows 已保护你的电脑”，点 **更多信息** → **仍要运行**。
2. 按当前用户安装到 `%LOCALAPPDATA%\Programs\E5 Developer Extend`，**不需要管理员权限**。
3. 完成后从开始菜单打开 **E5 Developer Extend**，托盘里会出现图标。

- **升级**：直接运行新版本的安装包。它会先停掉托盘和后台任务，换完文件再把后台任务启动起来。
- **卸载**：在“设置 → 应用”里卸载。卸载会先删除后台任务，再删除程序；
  你的配置、密钥和日志**保留**在 `%APPDATA%\e5extend` 和 `%LOCALAPPDATA%\e5extend`，要彻底清除就手动删掉这两个文件夹。
- 静默安装（批量部署）：`setup.exe /S`；静默卸载：`"%LOCALAPPDATA%\Programs\E5 Developer Extend\uninstall.exe" /S`。

只用命令行：把 zip 里的 `e5extend.exe` 和 `e5extend-bg.exe` 解压到**同一个固定目录**（例如 `C:\Users\你\bin\`，不要放在“下载”或临时目录），
再按 [getting-started.md](getting-started.md) 操作。`e5extend-bg.exe` 是后台任务用的不弹窗口版本。

## macOS

1. 打开 dmg，把 **E5 Developer Extend** 拖进 **Applications**（应用程序）。**一定要先拖进去再打开**：
   直接在 dmg 里或“下载”文件夹里运行时，macOS 会把它放在一个临时位置，后台服务会拒绝安装。
2. 第一次打开时 macOS 会提示“无法验证开发者”。点 **完成**，然后打开
   **系统设置 → 隐私与安全性**，在页面下方找到 E5 Developer Extend，点 **仍要打开**，输入密码确认。
   （macOS 14 及更早的版本也可以在 Finder 里按住 Control 点击应用，选“打开”。）
3. E5 Developer Extend 只在菜单栏显示图标，不在 Dock 里出现；打开主窗口时才有 Dock 图标。

- **升级**：退出 E5 Developer Extend，用新版本替换 Applications 里的旧版本。后台服务（LaunchAgent）指向的路径不变，下次重启就用新版本。
- **卸载**：先在应用里删除后台服务（或在终端里运行 `"/Applications/E5 Developer Extend.app/Contents/MacOS/e5extend" service uninstall`），
  并在设置里关掉“开机启动”，再把应用拖进废纸篓。配置和数据在 `~/Library/Application Support/e5extend`。

只用命令行：解压 tar 包，把 `e5extend` 放到固定位置（例如 `/usr/local/bin`）。用浏览器下载的文件带隔离属性，先去掉：

```bash
xattr -d com.apple.quarantine e5extend
```

## Linux

### deb / rpm

```bash
# Ubuntu / Debian：桌面版要两个包都装；服务器只装第一个
sudo apt install ./e5extend_*_amd64.deb ./e5extend-desktop_*_amd64.deb
# Fedora
sudo dnf install ./e5extend-*.x86_64.rpm ./e5extend-desktop-*.x86_64.rpm
```

桌面版需要 GTK4 和 WebKitGTK 6.0，所以要 Ubuntu 24.04、Debian 13、Fedora 39 或更新的版本。装好后在应用菜单里打开 **E5 Developer Extend**。

- **升级**：两个包**一起**装新版本（桌面包要求命令行包是同一版本，只装一个的话 apt 会把桌面包删掉），
  然后重启后台服务，让它换用新程序：`systemctl --user restart e5extend`。
- **卸载**：**先**用安装服务的那个用户运行 `e5extend service uninstall`，并在 E5 Developer Extend 的设置里关掉“开机启动”，再卸载包。
  安装包脚本以 root 运行，管不到每个用户自己的 systemd 服务；顺序反了的话，服务会因为找不到程序反复失败。
  配置和数据在 `~/.config/e5extend` 和 `~/.local/share/e5extend`，卸载不会删除。

### 其他发行版

用 tar 包里的命令行程序，放到 `~/.local/bin/`，按 [getting-started.md](getting-started.md) 和 [background.md](background.md) 设置 `systemd --user` 服务。

暂时没有 AppImage：AppImage 每次运行时程序都在一个临时路径下，后台服务和开机启动都会指向一个过后就不存在的位置，
不适合需要长期后台运行的程序。

## 升级

程序会**每天自动查一次**有没有新版本（后台引擎里，访问 GitHub 的公开接口，不上传任何你的数据），有就在概览页顶部显示横幅，
托盘菜单里多一项“升级到 x.y.z”，也可以通过告警渠道通知你（事件 `update_available`，每个版本只通知一次）。
**从不静默自动升级**：要你点“立即升级”或运行 `e5extend update install`。

- 桌面版：概览页的横幅点 **立即升级**，会显示下载、验证、安装的进度；“稍后”只对这个版本隐藏横幅。“关于”页能看到当前和最新版本，点 **立即检查** 马上查一次。
- 命令行：`e5extend update check` 看有没有新版本；`e5extend update install` 升级（`--yes` 跳过确认）。
- 不想要每天的检查：`config.toml` 里写 `[update]` `check = false`；想收到预发布版本写 `channel = "prerelease"`。手动检查不受影响。

升级前程序会**先验证发布签名，再校验文件的 SHA-256**，通过了才替换；任何一步不对都不会改动现有程序。不同安装方式的做法：

| 你的安装方式 | 一键升级做什么 |
|---|---|
| Windows 安装包 | 下载新的安装包并静默运行它，本程序随即退出；安装程序停掉托盘和后台任务、换文件、重新启动它们 |
| Windows 命令行 zip | 把 `e5extend.exe`、`e5extend-bg.exe` 换成新版本（正在运行的旧文件先改名为 `.old`，下次清理），然后重启后台任务 |
| macOS 应用 | 挂载 dmg，替换 `/Applications` 里的应用，重启后台服务；之后在应用里点“立即重启”。应用所在目录没有写权限时会告诉你怎么手动拖 |
| macOS / Linux 命令行 tar 包 | 原子替换 `e5extend`，重启后台服务（`systemctl --user restart e5extend` / launchd）；目录没有写权限时给出 `sudo install` 命令 |
| Linux deb / rpm | 一次装好命令行包和桌面包。桌面版通过 `pkexec` 弹出系统的授权窗口；命令行不是 root 时**只打印**要你运行的 `sudo apt-get install -y …` 命令（下载的文件留在临时目录）。装完重启后台服务 |
| Docker | 不能自己升级：`docker pull ghcr.io/kkazuhak/e5-developer-extend:<版本>` 后重建容器 |

没有签名的版本（发布时没有配置签名密钥）、开发构建，以及无法识别的安装方式，只会提示有新版本并给出发布页链接，不会自动安装。
想自己验证下载的文件：`minisign -Vm SHA256SUMS -P <公钥>`，公钥在 [internal/update/pubkey.go](../../internal/update/pubkey.go)，再 `sha256sum --ignore-missing -c SHA256SUMS`。
设计细节见 [design/update.md](../design/update.md)。

## 从源码构建

见 [dev-setup.md](../dev-setup.md)。发布包的构建脚本在 `scripts/release/`，说明见 [design/release.md](../design/release.md)。
