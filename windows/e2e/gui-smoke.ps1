# 界面冒烟测试（CI 的 Windows 虚拟机上运行，安装包已装好）：
#   1. 没有订阅时启动 App → 截图（引导页）
#   2. 写入一份指向本机测试节点的订阅、打开"启动时自动连接"→ 再启动 App → 截图（已连接）
# 截图保存到 $OutDir，由工作流上传为 artifact。
param([string]$OutDir = "$env:RUNNER_TEMP\screens")

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$install = Join-Path $env:ProgramFiles "MyTizi"
$app = Join-Path $install "MyTizi.exe"
$support = Join-Path $env:APPDATA "MyTizi"
Add-Type -AssemblyName System.Windows.Forms, System.Drawing

function Save-Screen([string]$name) {
  $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  $bmp = New-Object System.Drawing.Bitmap $bounds.Width, $bounds.Height
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
  $bmp.Save((Join-Path $OutDir "$name.png"), [System.Drawing.Imaging.ImageFormat]::Png)
  $g.Dispose(); $bmp.Dispose()
  Write-Host "截图：$name.png（$($bounds.Width)x$($bounds.Height)）"
}

function Start-App([string]$name, [int]$wait) {
  $p = Start-Process -FilePath $app -PassThru
  Start-Sleep -Seconds $wait
  if ($p.HasExited) { throw "MyTizi.exe 启动后退出了，退出码 $($p.ExitCode)" }
  Save-Screen $name
  return $p
}

# 1. 引导页
Remove-Item -Recurse -Force $support -ErrorAction SilentlyContinue
$p = Start-App "1-onboarding" 12
Stop-Process -Id $p.Id -Force
Start-Sleep -Seconds 2

# 2. 已连接：先起测试节点（本机 HTTP 代理，出站绑在物理网卡上）
$iface = (Get-NetAdapter -InterfaceIndex (Get-NetRoute -DestinationPrefix 0.0.0.0/0 | Sort-Object RouteMetric | Select-Object -First 1).InterfaceIndex).Name
$nodeDir = Join-Path $env:RUNNER_TEMP "gui-node"
New-Item -ItemType Directory -Force -Path $nodeDir | Out-Null
@"
mixed-port: 17890
bind-address: "127.0.0.1"
allow-lan: false
mode: rule
ipv6: false
geo-auto-update: false
interface-name: "$iface"
dns:
  enable: true
  ipv6: false
  default-nameserver: ["8.8.8.8#$iface"]
  nameserver: ["8.8.8.8#$iface"]
rules: ["MATCH,DIRECT"]
"@ | Set-Content -Encoding utf8 (Join-Path $nodeDir "node.yaml")
$node = Start-Process -FilePath (Join-Path $install "core\mihomo.exe") -ArgumentList "-d", "`"$nodeDir`"", "-f", "`"$nodeDir\node.yaml`"" -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 3

New-Item -ItemType Directory -Force -Path $support | Out-Null
$now = (Get-Date).ToUniversalTime().AddHours(-3).ToString("yyyy-MM-ddTHH:mm:ssZ")
@"
proxies:
  - {name: "香港-01", type: http, server: 127.0.0.1, port: 17890}
  - {name: "日本-02", type: http, server: 127.0.0.1, port: 17890}
  - {name: "美国-03", type: http, server: 192.0.2.1, port: 9}
"@ | Set-Content -Encoding utf8 (Join-Path $support "subscription.txt")
@"
{
  "subscription": {
    "url": "https://sub.example.com/api/v1/client/subscribe?token=demo",
    "name": "我的订阅",
    "addedAt": "$now",
    "updatedAt": "$now",
    "nodes": [
      {"name": "香港-01", "type": "Http", "udp": false},
      {"name": "日本-02", "type": "Http", "udp": false},
      {"name": "美国-03", "type": "Http", "udp": false}
    ],
    "usage": {"upload": 1073741824, "download": 41943040000, "total": 199990000000, "expire": "2027-01-18T00:00:00Z"}
  },
  "strictRoute": true,
  "healthCheckURL": "https://www.gstatic.com/generate_204",
  "autoConnectOnLaunch": true,
  "autoCheckUpdates": false
}
"@ | Set-Content -Encoding utf8 (Join-Path $support "settings.json")

$p = Start-App "2-connected" 25
$log = Join-Path $env:LOCALAPPDATA "MyTizi\Logs\app.log"
if (Test-Path $log) { Get-Content $log -Tail 30 }
$status = & sc.exe query MyTiziHelper
Write-Host $status
Stop-Process -Id $p.Id -Force
Stop-Process -Id $node.Id -Force -ErrorAction SilentlyContinue
