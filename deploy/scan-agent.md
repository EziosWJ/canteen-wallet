# Linux Scan Agent 最简启动

## 1. 准备

运行 `task build` 得到 `build/canteen-scan-agent`；将它复制到扫码机器。扫码枪支持串口（USB CDC ACM、USB 转串口）、evdev 或 boot-keyboard hidraw；当前 evdev 读取按 Linux amd64 实现。串口运行用户须有设备读写权限，evdev/hidraw 须有读取权限。

后台打开“终端与扫码事件”，填写名称创建终端，点击“管理”复制 key 或下载配置。下载前可选择扫码设备模式、填写扫码机器的设备路径，以及选择串口波特率；默认是 `serial`、`/dev/ttyACM0`、9600。设备设置用于本次下载，不保存到服务端。服务端地址使用扫码机器可访问的 HTTP 或 HTTPS 地址，同机和分机均可；无需开放内部 8081 端口。若分机部署，须将后台默认的 localhost 地址改为实际服务器地址。

## 2. 启动

在项目内测试时，根目录的 `terminal.env` 已配置本地服务端、`/dev/ttyACM0` 串口和 115200 波特率。先启动后台，再运行：

```sh
task agent:run
```

该任务仅编译扫码代理，加载根目录的 `terminal.env` 后以前台方式运行；按 Ctrl+C 停止。单独编译可用 `task agent:build`。测试配置纳入版本控制，终端 key 重置后需更新文件。

部署到扫码机器时，可直接通过环境变量启动：

```sh
CANTEEN_SERVER_URL=http://192.168.1.10:5001 \
CANTEEN_TERMINAL_KEY='trm_替换为后台复制的完整key' \
CANTEEN_SCANNER_DEVICE=/dev/ttyACM0 \
CANTEEN_SCANNER_MODE=serial \
CANTEEN_SCANNER_BAUD=9600 \
  ./canteen-scan-agent
```

若使用下载的 `terminal.env`，确认设备路径和波特率与实际扫码枪一致，然后启动：

```sh
chmod 600 terminal.env
set -a
. ./terminal.env
set +a
./canteen-scan-agent
```

扫码机器的浏览器打开 `http://127.0.0.1:8090/terminal`。重置 key 后更新环境变量或配置文件并重启 Agent；停用终端会拒绝其 API 请求，启用后当前 key 恢复可用。

## 可选与兼容配置

| 环境变量 | 用途 |
| --- | --- |
| `CANTEEN_SCANNER_MODE` | `serial`、`evdev` 或 `hidraw`；未设置时，`ttyACM*`、`ttyUSB*`、`ttyS*` 路径使用串口，`/event` 路径使用 evdev，其余使用 hidraw；稳定别名应显式设置模式 |
| `CANTEEN_SCANNER_BAUD` | 串口波特率，默认 9600；支持 1200、2400、4800、9600、19200、38400、57600、115200、230400 |
| `CANTEEN_DISPLAY_ADDR` | 本地页面监听，默认 `127.0.0.1:8090` |
| `CANTEEN_TTS_COMMAND` | 语音程序的绝对路径，可不配置 |
| `CANTEEN_TERMINAL_CREDENTIAL_FILE` | 未设置新 key 变量时，从此文件读取 key |
| `CANTEEN_INTERNAL_URL` | 未设置统一地址时，内部 API 地址，默认 `http://127.0.0.1:8081` |
| `CANTEEN_PUBLIC_URL` | 未设置统一地址时，Web 地址，默认 `http://127.0.0.1:5001` |

`CANTEEN_SERVER_URL` 优先于两个旧地址；`CANTEEN_TERMINAL_KEY` 优先于凭据文件。新变量设置为空或非法值时会报错，不会静默使用旧配置。统一地址可包含反向代理前缀，例如 `https://example.com/canteen`；该代理须将此前缀映射到服务端根路径。

串口使用 8N1，无软/硬件流控；扫码枪须在每次扫码后发送 CR、LF 或 CRLF。超过 128 字节的整条输入会被丢弃，不提交截断内容。evdev 使用 `/dev/input/eventX` 路径和 `CANTEEN_SCANNER_MODE=evdev`；hidraw 使用 `/dev/hidrawX` 路径和 `CANTEEN_SCANNER_MODE=hidraw`。扫码枪路径与设备权限由实施者配置；拔插后 Agent 会尝试重新连接，不提交拔插前未结束的输入。

Linux 串口通常属于 `dialout` 组。若 `ls -l /dev/ttyACM0` 显示 `root dialout`、权限为 `crw-rw----`，将运行用户加入 `dialout` 后重新登录，并重新启动 Agent/IDE。systemd 示例为服务附加 `input` 和 `dialout` 组。

WSL 2 还需要通过 usbipd-win 将 USB 设备连接到 WSL。重启 WSL 后，在 Windows PowerShell 用 `usbipd list` 确认 BUSID，并执行 `usbipd attach --wsl --busid <BUSID>` 重新连接。普通 WSL 终端有权限，但开发工具沙箱看不到串口时，实机联调须在获准访问设备的环境中运行。

Agent 启动即发送心跳，之后每 10 秒发送；待确认消费每 2 秒查询一次结果，重启后恢复查询。网络失败时显示离线，不缓存扫码后补扣；请求超时不代表服务端一定未处理，不能自行假定失败后重复扣款。

旧终端只有凭据哈希，升级后原 key 仍可用，后台不能还原其原文；可继续使用原文件，或在后台重置取得新 key。CLI `provision-terminal` 保留为备用，新创建凭据也可从后台取回。

自制客户端与 ESP32 调用方式见 [终端 API](../docs/terminal-api.md)。
