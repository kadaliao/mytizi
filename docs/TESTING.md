# 真机回归清单

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
7. 睡眠/唤醒、Wi-Fi 与有线切换后网页应能继续打开。
8. 卸载：「⋯ → 高级设置 → 卸载后台组件」。
9. 检查更新：「⋯ → 检查更新…」应给出“已是最新版本”或“发现新版本”；断网时应提示失败而不是卡住。自动检查每天最多一次，可在「高级设置」里关闭。
10. 线路全挂：把订阅换成一个不可达的节点后连接，约 10 秒内主界面应出现“所有线路都连不上”提示；「复制诊断信息」应把结论写成“节点服务器连不上（直连正常）”，而不是泛化的“所有线路测速都失败”。
