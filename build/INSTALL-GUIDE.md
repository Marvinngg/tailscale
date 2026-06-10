# 263onet 私网 Tailscale 安装指南

> 适用于 Windows / macOS / Linux，安装后即可接入 263onet 私网。
> 遇到问题联系 Marvin。

---

## 你的登录密钥

```
你的密钥会在单独消息中发给你，格式类似 hskey-auth-xxxxxx
```

> 密钥 7 天内有效，同一密钥可以给同一用户的多台设备使用。已登录设备不受过期影响。

---

## Windows

### 方式一：一行命令（推荐，新装 / 更新通用）

**以管理员身份打开 PowerShell**（开始菜单搜 PowerShell → 右键"以管理员身份运行"），然后：

**新用户首装**（带密钥）：

```powershell
& ([scriptblock]::Create((irm https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag4/install.ps1))) -Key 你的密钥
```

**已在网内、只是更新版本**（无需密钥）：

```powershell
irm https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag4/install.ps1 | iex
```

一行完成：装 CA 证书 → 写 hosts → 安装/更新客户端 → 自动登录（老用户复用身份，无需重新登录）。

3. 验证：执行

```
tailscale status
```

看到 `100.x.x.x` 开头的 IP 即成功。

> 已装过官方 Tailscale 的机器也能直接跑上面的命令更新，节点身份保留。

### 方式二：手动安装（脚本失败时备用）

1. **装 CA 证书**：双击 `ca.crt` → 安装证书 → 存储位置选"本地计算机" → 放入"受信任的根证书颁发机构"
2. **写 hosts**：管理员记事本打开 `C:\Windows\System32\drivers\etc\hosts`，末尾加一行：

```
114.141.178.114 hs.263onet.com
```

3. **安装客户端**：右键 `setup.exe` → 以管理员身份运行
4. **登录**：管理员 cmd 执行：

```
tailscale up --login-server=https://hs.263onet.com:8443 --auth-key=你的密钥 --accept-routes --accept-dns --reset --exit-node=100.96.0.1 --exit-node-allow-lan-access
```

### 常用操作

```
tailscale status                          # 查看状态
tailscale set --exit-node=100.96.0.1      # 开启全流量代理（出口节点）
tailscale set --exit-node=                # 关闭全流量代理
```

---

## macOS

### 一行命令安装（推荐）

打开终端，执行：

```
curl -fsSL https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag4/install.sh -o /tmp/install.sh && sudo bash /tmp/install.sh --key=你的密钥
```

一键完成：下载二进制 → 装 CA 证书 → 写 hosts → 启动守护进程 → 自动登录。

安装完成后验证：

```
tailscale status
```

### 如果 curl 下载慢

先浏览器下载 install.sh，再本地执行：

```
sudo bash ~/Downloads/install.sh --key=你的密钥
```

### 常用操作

```
tailscale status                          # 查看状态
tailscale set --exit-node=100.96.0.1      # 开启全流量代理
tailscale set --exit-node=                # 关闭全流量代理
```

### 卸载

```
curl -fsSL https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag4/uninstall.sh -o /tmp/uninstall.sh && sudo bash /tmp/uninstall.sh
```

---

## Linux (amd64)

> 目前只提供 amd64 二进制。arm64 用户请联系 Marvin 单独编译。

### 第 1 步：装 CA 证书

```bash
sudo tee /usr/local/share/ca-certificates/263onet-ca.crt << 'CERT'
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
CERT

sudo update-ca-certificates
```

输出应包含 `1 added` 即成功。

### 第 2 步：写 hosts

```bash
grep -q 'hs.263onet.com' /etc/hosts || echo '114.141.178.114 hs.263onet.com' | sudo tee -a /etc/hosts
```

### 第 3 步：下载并安装二进制

```bash
RELEASE=https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag4

sudo curl -fSL -o /usr/local/bin/tailscaled "${RELEASE}/tailscaled-linux-amd64"
sudo curl -fSL -o /usr/local/bin/tailscale  "${RELEASE}/tailscale-linux-amd64"
sudo chmod 755 /usr/local/bin/tailscaled /usr/local/bin/tailscale
```

如果 GitHub 下载慢，让 Marvin 单独发二进制文件，scp 上去放到 `/usr/local/bin/` 即可。

### 第 4 步：创建 systemd 服务

```bash
sudo tee /etc/systemd/system/tailscaled.service << 'UNIT'
[Unit]
Description=Tailscale node agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/tailscaled --state=/var/lib/tailscale/tailscaled.state --socket=/var/run/tailscale/tailscaled.sock
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

sudo mkdir -p /var/lib/tailscale /var/run/tailscale
sudo systemctl daemon-reload
sudo systemctl enable --now tailscaled
```

### 第 5 步：登录

```bash
sudo tailscale up \
  --login-server=https://hs.263onet.com:8443 \
  --auth-key=你的密钥 \
  --accept-routes \
  --accept-dns \
  --reset \
  --exit-node=100.96.0.1 \
  --exit-node-allow-lan-access
```

### 验证

```bash
tailscale status
```

看到 `100.x.x.x` 开头的 IP 即成功。

### 常用操作

```bash
tailscale status                          # 查看状态
tailscale set --exit-node=100.96.0.1      # 开启全流量代理
tailscale set --exit-node=                # 关闭全流量代理
sudo journalctl -u tailscaled -f          # 查看日志
```

### 卸载

```bash
sudo systemctl disable --now tailscaled
sudo rm /usr/local/bin/tailscaled /usr/local/bin/tailscale
sudo rm /etc/systemd/system/tailscaled.service
sudo systemctl daemon-reload
sudo rm -rf /var/lib/tailscale /var/run/tailscale
sudo rm /usr/local/share/ca-certificates/263onet-ca.crt
sudo update-ca-certificates --fresh
# 删 hosts 里的 hs.263onet.com 行
sudo sed -i '/hs.263onet.com/d' /etc/hosts
```

---

## 常见问题

**Q: tailscale up 报 TLS/certificate 错误？**
A: CA 证书没装成功。Windows 检查 certutil；macOS 检查钥匙串；Linux 检查 `update-ca-certificates` 输出。

**Q: 连接超时？**
A: hosts 没写。确认 `ping hs.263onet.com` 能解析到 `114.141.178.114`。

**Q: 已装了官方 Tailscale 怎么办？**
A: 必须先卸载官方版，再用本指南安装。Windows 跑 uninstall.bat 后重启；macOS 和 Linux 先停服务再删二进制。

**Q: 不想走出口节点（全流量代理）？**
A: 登录时去掉 `--exit-node` 和 `--exit-node-allow-lan-access` 参数即可，私网内设备互访不受影响。
