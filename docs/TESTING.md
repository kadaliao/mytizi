# 真机回归清单

## macOS

涉及 root、TUN 和系统网络设置的部分无法在单元测试里覆盖，改动网络相关代码后按下面的顺序手动过一遍。

> 先退出其他会开 TUN 的代理软件（Clash Verge 等）。两个 TUN 同时存在会互相抢路由与 DNS，结果没有意义。

1. 打开 `dist/MyTizi.app`，添加订阅，点连接并完成授权。
2. 确认接管生效：
   ```sh
   ifconfig | grep -B1 "198.18.0.1"          # 出现 utunN
   scutil --dns | sed -n 1,12p               # 第一个 resolver 的 nameserver 是 198.18.0.2
   networksetup -getinfo Wi-Fi | grep IPv6   # IPv6: Off
   tail -n 20 /Library/Logs/MyTizi/mihomo.log
   ```
3. 「遇到问题 → 复制诊断信息」，看「实机自检」表：国内站和国内 CDN 应为直连，海外站应走代理，DNS 应为 fake-ip。
4. 实际使用：浏览器、微信（含图片）、飞书、YouTube、终端 `curl`。
5. 断开后确认恢复：
   ```sh
   networksetup -getdnsservers Wi-Fi         # 回到原值（通常是 "There aren't any DNS Servers set"）
   networksetup -getinfo Wi-Fi | grep IPv6   # 回到 Automatic
   ls "/Library/Application Support/MyTizi/network-state.json"   # 应不存在
   ```
6. 崩溃恢复：连接后 `sudo pkill -9 mihomo`，几秒内网络应自动恢复，App 显示「连接意外中断」。
7. 睡眠/唤醒、Wi-Fi 与有线切换后网页应能继续打开。切换后 `/Library/Logs/MyTizi/helper.log` 应出现一行「出口网卡：en0 → en1」之类的记录，诊断报告「后台组件」一行的「出口」应是新网卡。
8. 与 Tailscale 共存（可选）：开着 Tailscale 连接，`ping` 一台 tailnet 设备应通，MagicDNS 名字应能解析；再在 Tailscale 里启用 exit node，诊断报告应出现「Tailscale 的出口节点（exit node）正在接管默认路由」的提醒，海外站仍应走代理（自检表）；关闭 exit node 后提醒消失。
9. 卸载：「⋯ → 高级设置 → 卸载后台组件」。
10. 检查更新：「⋯ → 检查更新…」应给出“已是最新版本”或“发现新版本”；断网时应提示失败而不是卡住。自动检查每天最多一次，可在「高级设置」里关闭。
11. 线路全挂：把订阅换成一个不可达的节点后连接，约 10 秒内主界面应出现“所有线路都连不上”提示；「复制诊断信息」应把结论写成“节点服务器连不上（直连正常）”，而不是泛化的“所有线路测速都失败”。

## Windows

CI 的端到端测试已经覆盖安装、连接、分流、崩溃恢复和卸载（见 `windows/README.md`）。下面是虚拟机测不到、需要在家用电脑上过一遍的部分。PowerShell 命令不需要管理员。

> 同样先退出其他会开 TUN 的代理软件（Clash Verge、v2rayN 等）。

1. 运行安装包。SmartScreen 拦截时点「更多信息 → 仍要运行」；完成页勾选「打开 MyTizi」。
2. 粘贴订阅、连接，确认接管生效：
   ```powershell
   Get-NetAdapter -Name MyTizi                                  # Status 为 Up
   Get-DnsClientServerAddress -InterfaceAlias MyTizi -AddressFamily IPv4   # 198.18.0.2
   Resolve-DnsName www.baidu.com -Type A                        # 198.18.x.x
   Get-Content "$env:ProgramData\MyTizi\logs\mihomo.log" -Tail 20
   ```
3. 「遇到问题 → 复制诊断信息」，看「实机自检」：国内站与国内 CDN 直连，海外走代理，DNS 为 fake-ip；有 IPv6 的网络里「IPv6 是否被拦截」应通过。
4. 实际使用：Edge/Chrome、微信（含图片）、飞书、YouTube、`curl.exe -I https://www.google.com`。任务栏网络图标不应出现"无 Internet"。
5. 关掉窗口：App 留在托盘；左键点托盘图标能重新打开；右键菜单的「断开/连接」可用。托盘「退出」时会询问断开还是保持。
6. 断开后：`Get-NetAdapter -Name MyTizi` 应找不到；`Resolve-DnsName www.baidu.com` 返回真实地址。
7. 崩溃恢复：连接后以管理员身份 `taskkill /F /IM mihomo.exe`，几秒内网络应恢复，App 显示「连接意外中断」。
8. 睡眠/唤醒、Wi-Fi 与有线切换后网页应能继续打开（服务在唤醒后会清理旧连接，`helper.log` 有一行「系统从睡眠中唤醒」）。
9. 与 Tailscale 共存（可选）：连着 Tailscale 再连接，`ping` 一台 tailnet 设备应通，MagicDNS 名字应能解析；开 exit node 后诊断应出现提醒，海外站仍走代理。
10. 高级设置：开关「登录 Windows 时自动打开」后注销再登录，MyTizi 应只出现在托盘里；关掉「严格接管」重新连接后，诊断应提示 DNS 可能泄漏。
11. 升级：装着旧版时运行新安装包，应自动关掉 App、停服务、完成后重新注册；订阅与设置保留。
12. 卸载：「设置 → 应用 → MyTizi → 卸载」。之后 `Get-Service MyTiziHelper` 应找不到，`C:\Program Files\MyTizi` 与 `C:\ProgramData\MyTizi` 都不存在；`%APPDATA%\MyTizi` 保留。
13. 杀毒软件：若提示「无法启动内置核心」，确认是杀毒软件拦截了 `core\mihomo.exe`，加信任后重试。
