Antigravity Tailscale - Windows
================================

## 一键安装（推荐）

1. 解压 zip 到本地目录（路径建议英文，例如 C:\Apps\TS\）
2. 右键 install.bat → 以管理员身份运行：

   install.bat --key=YOUR_AUTH_KEY

   一键完成：
     - 装 263onet 自签 CA 到 LocalMachine\Root
     - 写 C:\Windows\System32\drivers\etc\hosts (hs.263onet.com → 114.141.178.114)
     - 跑 setup.exe（官方 MSI + 我们 fork 版 binary 覆盖）
     - 自动 tailscale up --login-server=https://hs.263onet.com:8443 --auth-key=...

3. 验证：

   tailscale status

可选参数：
   --server=URL        Headscale URL（默认 https://hs.263onet.com:8443）
   --exit-node=IP      出口节点 IP（默认 100.96.0.1）
   --no-exit-node      不设出口节点
   --no-hosts-write    不写 hosts 文件

## 已装过官方 Tailscale 的用户

先跑 uninstall.bat 彻底清理（含 MSI uninstall），然后重启 Windows，再走上面"一键安装"。

## 手动安装（不推荐，留作对照）

如果 install.bat 失败，可分步：
1. 双击 ca.crt → 安装证书 → 本地计算机 → 受信任的根证书颁发机构
2. 管理员记事本编辑 C:\Windows\System32\drivers\etc\hosts，加一行：
   114.141.178.114 hs.263onet.com
3. 右键 setup.exe → 以管理员身份运行
4. 管理员 cmd 执行：

   tailscale up --login-server=https://hs.263onet.com:8443 --auth-key=YOUR_KEY ^
     --accept-routes --accept-dns --reset ^
     --exit-node=100.96.0.1 --exit-node-allow-lan-access

## 功能

### 出口节点（全流量代理）

   tailscale set --exit-node=100.96.0.1

关闭出口节点：

   tailscale set --exit-node=

### 文件传输

发送文件给其他设备（用 Tailscale IP）：

   tailscale fs send 文件名.txt 100.96.0.4

查看收到的文件历史：

   tailscale fs inbox

收到的文件自动保存到 Downloads 文件夹。

浏览远程设备的共享文件库：

   tailscale fs ls 100.96.0.4:

下载远程文件：

   tailscale fs get 100.96.0.4:public/文件名.txt

### 语音转文字（Voice Relay）

将 Windows 麦克风录音发送到 Mac mini 做语音识别，
文字自动出现在 Mac 光标处。

首次设置（只需一次）：

   tailscale voice setup --target 100.96.0.4:9800

之后每次开机自动启动，或手动运行：

   tailscale voice

按住右 Alt 键说话，松开发送。

查看设置：

   tailscale voice status

关闭自动启动：

   tailscale voice stop

## 卸载

右键 uninstall.bat → 以管理员身份运行。

卸载会完整清理：
  - kill 所有 Tailscale 进程 + 停服务
  - MSI 卸载（避免下次重装 1603 错误）
  - sc delete Tailscale 服务
  - 删 C:\Program Files\Tailscale
  - 删 %ProgramData%\Tailscale + %APPDATA%\Tailscale
  - 删启动项
  - 删 263onet CA 信任
  - 删 hosts 里的 hs.263onet.com 行
