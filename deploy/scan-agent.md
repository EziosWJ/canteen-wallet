# Linux Scan Agent 最简启动

## 1. 准备

运行 `task build` 得到 `build/canteen-scan-agent`；将它复制到扫码机器。当前 evdev 读取按 Linux amd64 实现，扫码枪需要兼容 evdev 或 boot-keyboard hidraw；运行用户须有设备读取权限。

后台打开“终端与扫码事件”，填写名称创建终端，点击“管理”复制 key 或下载配置。服务端地址使用后台可访问的 HTTP 或 HTTPS 地址，同机和分机均可；无需开放内部 8081 端口。

## 2. 启动

```sh
CANTEEN_SERVER_URL=http://192.168.1.10:5001 \
CANTEEN_TERMINAL_KEY='trm_替换为后台复制的完整key' \
CANTEEN_SCANNER_DEVICE=/dev/input/eventX \
CANTEEN_SCANNER_MODE=evdev \
  ./canteen-scan-agent
```

若使用下载的 `terminal.env`，先填写实际设备路径，然后启动：

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
| `CANTEEN_SCANNER_MODE` | `evdev` 或 `hidraw`；未设置时根据路径判断 |
| `CANTEEN_DISPLAY_ADDR` | 本地页面监听，默认 `127.0.0.1:8090` |
| `CANTEEN_TTS_COMMAND` | 语音程序的绝对路径，可不配置 |
| `CANTEEN_TERMINAL_CREDENTIAL_FILE` | 未设置新 key 变量时，从此文件读取 key |
| `CANTEEN_INTERNAL_URL` | 未设置统一地址时，内部 API 地址，默认 `http://127.0.0.1:8081` |
| `CANTEEN_PUBLIC_URL` | 未设置统一地址时，Web 地址，默认 `http://127.0.0.1:5001` |

`CANTEEN_SERVER_URL` 优先于两个旧地址；`CANTEEN_TERMINAL_KEY` 优先于凭据文件。新变量设置为空或非法值时会报错，不会静默使用旧配置。统一地址可包含反向代理前缀，例如 `https://example.com/canteen`；该代理须将此前缀映射到服务端根路径。

hidraw 设备使用 `/dev/hidrawX` 路径和 `CANTEEN_SCANNER_MODE=hidraw`。扫码枪路径与设备读取权限由实施者配置；拔插后 Agent 会尝试重新连接。

Agent 启动即发送心跳，之后每 10 秒发送；待确认消费每 2 秒查询一次结果，重启后恢复查询。网络失败时显示离线，不缓存扫码后补扣；请求超时不代表服务端一定未处理，不能自行假定失败后重复扣款。

旧终端只有凭据哈希，升级后原 key 仍可用，后台不能还原其原文；可继续使用原文件，或在后台重置取得新 key。CLI `provision-terminal` 保留为备用，新创建凭据也可从后台取回。

自制客户端与 ESP32 调用方式见 [终端 API](../docs/terminal-api.md)。
