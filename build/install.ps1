#Requires -Version 5.1
<#
  Antigravity Tailscale Installer / Updater (Windows)
  对标 macOS install.sh：一条命令，新老用户通吃。

  - 老用户（已有 server-state.conf）：纯更新二进制，复用节点身份，无需 key
  - 新用户（无 state）：需 -Key 注册

  用法（管理员 PowerShell）：
    # 一行安装/更新（最新版）：
    irm https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag4/install.ps1 | iex

    # 新用户带 key：
    & ([scriptblock]::Create((irm https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag4/install.ps1))) -Key hskey-auth-xxxx

  参数：
    -Key KEY          Headscale 预授权 key（新用户首装必填）
    -Server URL       Headscale 地址（默认 https://hs.263onet.com:8443）
    -ExitNode IP      出口节点（默认 100.96.0.1）
    -NoExitNode       不走出口节点
    -NoHostsWrite     不写 hosts
    -Tag TAG          release tag（默认 v1.94.2-ag4）
#>
param(
  [string]$Key       = "",
  [string]$Server    = "https://hs.263onet.com:8443",
  [string]$ExitNode  = "100.96.0.1",
  [switch]$NoExitNode,
  [switch]$NoHostsWrite,
  [string]$Tag       = "v1.94.2-ag4"
)

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$HeadscaleHost = "hs.263onet.com"
$HeadscaleIP   = "114.141.178.114"
$InstallDir    = "C:\Program Files\Tailscale"
$StateConf     = "C:\ProgramData\Tailscale\server-state.conf"
$ReleaseUrl    = "https://github.com/Marvinngg/tailscale/releases/download/$Tag"
if ($NoExitNode) { $ExitNode = "" }

function Info($m) { Write-Host "  $m" }

# ---- admin 检查 + 自动提权 ----
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
  Write-Host "需要管理员权限，正在提权重启..."
  $argLine = "-NoProfile -ExecutionPolicy Bypass -Command `"& ([scriptblock]::Create((irm $ReleaseUrl/install.ps1)))`""
  if ($Key)         { $argLine += " -Key $Key" }
  if ($NoExitNode)  { $argLine += " -NoExitNode" }
  if ($NoHostsWrite){ $argLine += " -NoHostsWrite" }
  Start-Process powershell -Verb RunAs -ArgumentList $argLine
  return
}

Write-Host ""
Write-Host "=== Antigravity Tailscale Installer (Windows) ==="
Write-Host "  Server:    $Server"
Write-Host "  Exit node: $(if($ExitNode){$ExitNode}else{'none'})"
Write-Host "  Tag:       $Tag"
Write-Host ""

# ---- 判定新老用户 ----
$Existing = Test-Path $StateConf
if (-not $Key -and -not $Existing) {
  Write-Host "Error: 新用户首装必须带 -Key（未发现 $StateConf）" -ForegroundColor Red
  Write-Host '用法: & ([scriptblock]::Create((irm .../install.ps1))) -Key hskey-auth-xxxx'
  return
}

# ---- [1/6] 下载二进制到临时目录（先下载，失败不破坏现有安装）----
Write-Host "[1/6] 下载二进制..."
$Tmp = Join-Path $env:TEMP ("ts-" + [guid]::NewGuid().ToString("N").Substring(0,8))
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null
try {
  Invoke-WebRequest "$ReleaseUrl/tailscaled-windows-amd64.exe" -OutFile "$Tmp\tailscaled.exe" -UseBasicParsing
  Invoke-WebRequest "$ReleaseUrl/tailscale-windows-amd64.exe"  -OutFile "$Tmp\tailscale.exe"  -UseBasicParsing
} catch {
  Write-Host "Error: 下载失败，现有安装未改动: $_" -ForegroundColor Red
  Remove-Item $Tmp -Recurse -Force -EA SilentlyContinue
  return
}
Info "下载完成"

# ---- [2/6] 装 263onet CA 到 LocalMachine\Root ----
Write-Host "[2/6] 安装 263onet CA 证书..."
$CaPem = @"
-----BEGIN CERTIFICATE-----
MIIFbzCCA1egAwIBAgIUFxzvTe4cYNMTVv6n9/kODXlVOFAwDQYJKoZIhvcNAQEL
BQAwRzELMAkGA1UEBhMCQ04xEDAOBgNVBAoMBzI2M29uZXQxJjAkBgNVBAMMHTI2
M29uZXQgSW50ZXJuYWwgSGVhZHNjYWxlIENBMB4XDTI2MDUwODA3MzUzN1oXDTM2
MDUwNTA3MzUzN1owRzELMAkGA1UEBhMCQ04xEDAOBgNVBAoMBzI2M29uZXQxJjAk
BgNVBAMMHTI2M29uZXQgSW50ZXJuYWwgSGVhZHNjYWxlIENBMIICIjANBgkqhkiG
9w0BAQEFAAOCAg8AMIICCgKCAgEAzHnVAdVpxukt9CHT3QtFZAbMz1Pe9Fr4Z0fC
YwjZSZIQHozReH4fFEbvmUf33+2Bg527QOKyVXnUjvCK8h4T6byD8Igt293kz3yw
dL9Pvn6YdZaJqkeagG0hvlrnTSAy/hLIUUcThPMdx4TwwMSuLtqkG+V/hXfNOAmp
7BhHDv3YzHYvYJ59Dijf+sIp7+wqYRmwvZ8MfNZDMfnHzqRwl27C2kZitY/fcZkr
I79c7OAenR6rdDJzjy9mxscKylga4cqiQvKem8OWnYBM4XMsJKGB8P2wUMfg63Ca
rCp9iSiIGpHeEyyuVm9yTCFYrmiImpVGRh9CB5zjlr3FBFzzY3TNXNDmczCcI11m
pYtd9XteALvWOo/Snz/YIvqHU2vToA+IAIP2smJQXFBmdv98Cta5n72JjbyvDhXO
CO+0OT04Yv3zgWDDfvEVQHiwu0Us46B/IxIEF8tY+e/Wbwt+EZskMwIREldz+lW2
AWFhf/Ol+GqklcSZ2XwN+m7uAt1XBiKTLDs7gNvBuUIvczTfMo/s+Do1R8ZylWZk
QmwnseMxPobkFnjVlPTQ2duJ/4D73OhjfNDq1YFaSMRDBWOKVYVZEKKRbp0CbQZ4
/t9Bx/CqUa7gri8NYypP6dISjQU/jB6HvsuaaVdxQrk9NedvZmxtmJprGnVS3hbC
tLJwSLUCAwEAAaNTMFEwHQYDVR0OBBYEFB23tWBo+PMUo+qUphOg80PQ/iSfMB8G
A1UdIwQYMBaAFB23tWBo+PMUo+qUphOg80PQ/iSfMA8GA1UdEwEB/wQFMAMBAf8w
DQYJKoZIhvcNAQELBQADggIBAI0be/wFVAll+Zoqk2nDm0eDxJ4pd1mof+plTkk7
xu9p+NjQk4DVnvHyxKTNBuS8I0Ebu6BeCeaUKH2oirjxIKoUGz5/MhWQ7oWOdbDp
mpye+w2P1tHb5qJHAqSuPr9w3ubpIbBnO0qGqGGGg/o9E9XC6x5LUbMMj/cfwEIi
LtsIqNF4t9cLKzn617wgiMj2O129zMM2ERYmLrl2j7Df9fKOm2fItC7kMGiatP4Z
oopxtx7o111wCIscQBALPXDJqQv9ZTSgmlc3vNSUJl/M4R8jxopARQGrR/ANyfu1
0WsQOeaUGD9iovBpn4BDty6ltPiSkKeAZ4mYQ43cFmbZvLYOx/b4AZ6LO8sU9jJG
Jx+4xN5jzDTqbB1+G2lRqMynh9KJeohBGlYOf4Zx5S5l8rAt7znwAslOAGqBNLFM
8TJQylNXkND+nv9cuizYUhsSocfk3eMZpBUY7TFSGW7PvH/l/HN6miIg03yz6efa
rOcmCht6S6K3L3l7h9iIIvdHypI64rS/YnSh39wUgRmkeKaOXMKXuPfsL5oUwh2P
tTaHklcUU06p6xKOJiAwF69CWxKYYsdhAjm/R+GAWJK+w5M6qIBq7ZzqLmdJrnSl
98Ll+Fa8j7dgsk7dE7hEcHw2kmDtzSK1TfZr4YYXjFXczf7ik2AbSvzfD9v9ssJG
94AL
-----END CERTIFICATE-----
"@
$CaPath = Join-Path $Tmp "263onet-ca.crt"
Set-Content -Path $CaPath -Value $CaPem -Encoding ASCII
$null = & certutil -addstore -f "Root" $CaPath 2>&1
if ($LASTEXITCODE -eq 0) { Info "CA 已信任" } else { Info "WARN: CA 导入失败（可能已存在）" }

# ---- [3/6] 写 hosts ----
if (-not $NoHostsWrite) {
  Write-Host "[3/6] 写 hosts..."
  $HostsFile = "$env:SystemRoot\System32\drivers\etc\hosts"
  $line = "$HeadscaleIP $HeadscaleHost"
  $content = Get-Content $HostsFile -EA SilentlyContinue
  if ($content -notmatch [regex]::Escape($HeadscaleHost)) {
    Add-Content -Path $HostsFile -Value "`r`n$line"
    Info "已写入: $line"
  } else { Info "hosts 已存在该条目" }
} else { Write-Host "[3/6] 跳过 hosts" }

# ---- [4/6] 停服务 + 装二进制 ----
Write-Host "[4/6] 停服务并替换二进制..."
$svc = Get-Service Tailscale -EA SilentlyContinue
if ($svc) { Stop-Service Tailscale -Force -EA SilentlyContinue }
Get-Process tailscale-ipn,tailscale,tailscaled -EA SilentlyContinue | Stop-Process -Force -EA SilentlyContinue
Start-Sleep -Seconds 2

New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
Copy-Item "$Tmp\tailscaled.exe" "$InstallDir\tailscaled.exe" -Force
Copy-Item "$Tmp\tailscale.exe"  "$InstallDir\tailscale.exe"  -Force
Info "二进制已更新"

# 注册服务（新装时；老用户已注册则跳过/幂等）
if (-not $svc) {
  & "$InstallDir\tailscaled.exe" install-system-daemon 2>&1 | Out-Null
  Info "服务已注册"
}

# ---- [5/6] 启动服务 + 连接 ----
Write-Host "[5/6] 启动服务..."
Start-Service Tailscale
Start-Sleep -Seconds 3

$tscli = "$InstallDir\tailscale.exe"
$exitArgs = @()
if ($ExitNode) { $exitArgs = @("--exit-node=$ExitNode","--exit-node-allow-lan-access") }

if ($Key) {
  Info "Mode: 注册（带 auth-key）"
  & $tscli up --login-server=$Server --auth-key=$Key --accept-routes --accept-dns --reset --unattended @exitArgs
} else {
  Info "Mode: 更新（复用现有身份，无需 key）"
}
Start-Sleep -Seconds 3

# ---- [6/6] 状态 ----
Write-Host "[6/6] 状态:"
& $tscli version
Write-Host ""
& $tscli status

Remove-Item $Tmp -Recurse -Force -EA SilentlyContinue
Write-Host ""
Write-Host "=== 完成 ===" -ForegroundColor Green
Write-Host "  Daemon: $InstallDir\tailscaled.exe"
Write-Host "  CLI:    $InstallDir\tailscale.exe"
