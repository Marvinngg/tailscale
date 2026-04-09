Antigravity Tailscale - macOS
==============================

## 安装（一行命令）

curl -fsSL https://raw.githubusercontent.com/Marvinngg/tailscale/antigravity/v1.94.2-macos-exitnode/build/install.sh | sudo bash -s -- --key=你的密钥

参数：
  --key=KEY          Headscale 认证密钥（必填）
  --server=URL       Headscale 地址（默认 https://hs.marvinai.qzz.io:8443）
  --exit-node=IP     出口节点（默认 100.64.0.1）
  --no-exit-node     不设出口节点

## 功能

### 查看状态

   tailscale status

### 出口节点

   tailscale set --exit-node=100.64.0.1
   tailscale set --exit-node=             # 关闭

### 文件传输

发送文件：

   tailscale fs send 文件名.txt 100.64.0.9

查看收到的文件：

   tailscale fs inbox

文件自动保存到 ~/Downloads/

浏览远程共享文件库：

   tailscale fs ls 100.64.0.9:

广播文件给所有在线设备：

   tailscale fs broadcast 文件名.txt

### 语音接收（WE 远程输入）

Mac mini 作为语音识别端，Windows 端按热键说话，
文字自动出现在 Mac 光标处。

需要安装并运行 WE 应用，配置 remote.enabled = true。

## 卸载

   sudo launchctl unload /Library/LaunchDaemons/com.tailscale.tailscaled.plist
   sudo rm /usr/local/bin/tailscaled /usr/local/bin/tailscale

## 日志

   tail -f /var/log/tailscaled.log
