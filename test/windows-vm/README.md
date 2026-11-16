# Windows 虚拟机测试

CI 测不到的 Windows 行为（登录时启动、有没有控制台窗口、桌面会话里的凭据管理器、睡眠和时钟跳跃），
用一台一次性的 KVM 虚拟机来测。

## 准备

- 镜像：Windows 11 IoT Enterprise LTSC 2024（`install.wim` 第 2 个映像）。
  其他 Windows 11 镜像也可以，改 `autounattend.xml.in` 里的 `/IMAGE/INDEX`，需要时再换产品密钥。
  模板里的是微软公开的 IoT Enterprise LTSC 2024 **KMS 客户端密钥（GVLK）**：只用来完成安装，不会激活系统。
- 生成测试用的 SSH 密钥和随机密码，替换模板里的 `@PUBKEY@`、`@PASSWORD@`，再把
  `autounattend.xml` 和 `e5setup.ps1` 打成一个小 ISO：

  ```bash
  xorriso -as mkisofs -J -r -V E5UNATTEND -o unattend.iso <目录>
  ```

- 两个 ISO 都用 `virsh vol-upload` 传进 libvirt 的 `default` 存储池，因为 QEMU 进程读不了用户主目录。

## 创建虚拟机

```bash
virt-install --connect qemu:///system --name test-win11-e5renew --memory 8192 --vcpus 4 \
  --cpu host-passthrough --os-variant win11 --boot hd,cdrom --tpm none \
  --disk pool=default,size=64,format=qcow2,bus=sata,sparse=yes \
  --disk vol=default/<windows>.iso,device=cdrom,bus=sata \
  --disk vol=default/<unattend>.iso,device=cdrom,bus=sata \
  --network network=default,model=e1000e --graphics vnc,listen=127.0.0.1 --noautoconsole
```

注意几点：

- `--os-variant win11` 默认使用带 Secure Boot 的 UEFI，可以正常启动。在这台主机上，**显式指定关闭 Secure Boot 的固件会被
  AppArmor 拒绝**（`cannot load AppArmor profile`），所以不要加 `firmware.feature…secure-boot…enabled=no`。
- UEFI 会显示“Press any key to boot from CD”，开机后马上用 `virsh send-key <名称> KEY_ENTER` 连按几次。
- 全程无人值守，大约 10 分钟后 SSH 可用：
  - 自动登录用户 `tester`（管理员）；
  - 首次登录时启用 OpenSSH，默认 shell 设为 PowerShell；
  - 关闭睡眠，把网络设为专用网络。

## 测试要点

- SSH 会话是“网络登录”，**读不到凭据管理器**。凡是和钥匙串、窗口有关的测试，都要用 `indesk.ps1`
  在桌面会话里运行：它会临时创建一个交互式计划任务，执行脚本并收集输出。
- SSH 会话里的 `$env:USERDOMAIN` 是 `WORKGROUP`，不是本机名。取当前用户要用
  `[Security.Principal.WindowsIdentity]::GetCurrent().Name`。
- 默认执行策略会阻止脚本运行，先执行 `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned`。

## 2026-11-16 结果（Windows 11 IoT Enterprise LTSC 2024，26100）

| 项目 | 结果 |
|---|---|
| `vault init` 把主密钥存进凭据管理器（`cmdkey` 能看到 `e5renew:master-key`） | ✓ |
| Client Secret 不以明文出现在磁盘上 | ✓ |
| `service install`：任务设置为 `PT0S`、电池下不停止、`IgnoreNew`、登录触发器加 `PT5M` 重复、受限权限运行 `e5renew-bg.exe` | ✓ |
| 引擎在桌面会话里运行，**没有控制台窗口**（截图确认，`MainWindowHandle=0`） | ✓ |
| 结束引擎进程后，计划任务自动重新拉起（这次 35 秒） | ✓ |
| 重启虚拟机：自动登录 1 秒后引擎由登录触发器启动，没有窗口 | ✓ |
| 关机时引擎正常退出（日志里有 `stop` 事件） | ✓ |
| 时钟前跳 3 小时：记录 `resume` 事件；后跳 3 小时：记录 `clock_change` 事件；进程一直没重启 | ✓ |
| 虚拟机暂停 3 分钟后恢复：引擎继续运行（客户机时钟也一起暂停了，所以没有跳跃事件，符合预期） | ✓ |
| `service uninstall`：任务删除，引擎停止，配置和 vault 保留 | ✓ |
| **发现的问题**：通过 SSH 执行 `doctor` 时，钥匙串读不到，程序却建议执行 `vault reset` | 已修复（提交 787e860） |
| `service status` 在 Windows 上显示 `running: unknown`（引擎心跳那一行能看出是否在运行） | 可以改进 |
