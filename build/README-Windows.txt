Antigravity Tailscale - Windows
================================

## 安装

1. 解压 zip
2. 双击 setup.exe（自动下载官方驱动并安装）
3. 安装完成后，打开命令行运行：

   tailscale up --login-server=https://hs.marvinai.qzz.io:8443 --auth-key=你的密钥

4. 验证：

   tailscale status

## 功能

### 出口节点（全流量代理）

   tailscale set --exit-node=100.64.0.1

关闭出口节点：

   tailscale set --exit-node=

### 文件传输

发送文件给其他设备（用 Tailscale IP）：

   tailscale fs send 文件名.txt 100.64.0.10

查看收到的文件历史：

   tailscale fs inbox

收到的文件自动保存到 Downloads 文件夹。

浏览远程设备的共享文件库：

   tailscale fs ls 100.64.0.10:

下载远程文件：

   tailscale fs get 100.64.0.10:public/文件名.txt

### 语音转文字（Voice Relay）

将 Windows 麦克风录音发送到 Mac mini 做语音识别，
文字自动出现在 Mac 光标处。

首次设置（只需一次）：

   tailscale voice setup --target 100.64.0.10:9800

之后每次开机自动启动，或手动运行：

   tailscale voice

按住右 Alt 键说话，松开发送。

查看设置：

   tailscale voice status

关闭自动启动：

   tailscale voice stop

## 卸载

双击 uninstall.bat（以管理员身份运行）
