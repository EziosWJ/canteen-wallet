# Linux Scan Agent 最简启动

## 1. 准备

运行 `task build` 得到 `build/canteen-scan-agent`；将它复制到扫码机器。Agent 通过串口读取扫码枪，支持 RS232、USB 转串口和 TTL 接口；运行用户须有串口设备读取权限，通常是将该用户加入 `dialout` 组（随附的 systemd 单元已设置 `SupplementaryGroups=dialout`）。

扫码枪与 Agent 之间有一份必须两端一致的参数契约：**115200 波特率、8 位数据、无校验、1 位停止位，并且扫码枪须配置帧结束后缀（推荐 CR、LF 或 CRLF，对应 Agent 的默认值 `any`）**。串口参数配错不会报错，只会读到乱码，现场表现为“扫码枪显示在线但扫不出结果”，因此这份契约需要逐项核对。

后台打开“终端与扫码事件”，填写名称创建终端，点击“管理”复制 key 或下载配置。服务端地址使用后台可访问的 HTTP 或 HTTPS 地址，同机和分机均可；无需开放内部 8081 端口。

## 2. 启动

```sh
CANTEEN_SERVER_URL=http://192.168.1.10:5001 \
CANTEEN_TERMINAL_KEY='trm_替换为后台复制的完整key' \
CANTEEN_SCANNER_DEVICE=/dev/ttyUSB0 \
CANTEEN_SCANNER_BAUD=115200 \
  ./canteen-scan-agent
```

若使用下载的 `terminal.env`，先填写实际串口路径，然后启动：

```sh
chmod 600 terminal.env
set -a
. ./terminal.env
set +a
./canteen-scan-agent
```

扫码机器的浏览器打开 `http://127.0.0.1:8090/terminal`。重置 key 后更新环境变量或配置文件并重启 Agent；停用终端会拒绝其 API 请求，启用后当前 key 恢复可用。

## 3. 串口设备

`CANTEEN_SCANNER_DEVICE` 必须显式填写，Agent 不会自动选择端口。优先使用 `/dev/serial/by-id/` 下的路径，避免插拔顺序让 `/dev/ttyUSB0` 与 `/dev/ttyUSB1` 互换；也可以沿用 udev 规则固定成一个稳定路径。

设备路径打不开时 Agent 每 2 秒重试，并把本机枚举到的候选端口写进日志，便于对照填写。Agent 以独占方式打开串口，因此端口被其他进程占用时（例如 `ttyUSB0` 上挂了 `serial-getty`）打开会失败，日志中的错误信息可用于确认这一情况。拔掉串口线后 Agent 显示离线并持续重连，恢复后自动继续读取，无需重启整机。

扫码枪在位但断电时，串口既不报错也不产生数据，终端页会一直显示扫码枪在线。这不是软件故障，也不表示扫码可用。

## 4. 帧结束判定

串口没有键码，一帧的结束由扫码枪自己配置的后缀决定：

- `CANTEEN_SCANNER_TERMINATOR`：`any`（默认，CR 与 LF 任一都算一帧结束）、`cr`、`lf`、`none`。扫码枪后缀为 CRLF 时无需改动，`any` 已经覆盖。
- `CANTEEN_SCANNER_IDLE_MS`：默认 `50`。扫码枪未配置任何后缀时，端口静默超过这个毫秒数即认为当前帧结束。取值 10–500。

帧内容先剥掉首尾的 NUL、CR、LF、空格和制表符，再检查中间是否含不可打印字节；含不可打印字节的帧整帧丢弃并记 WARN 日志。因此被配置成定长补位（短码用 NUL 补齐）的扫码枪可以直接使用，而波特率错配产生的乱码会被丢弃。丢弃日志带字节数与一次性的排查提示；若持续出现丢弃，先核对两端波特率，再核对扫码枪后缀设置与 `CANTEEN_SCANNER_TERMINATOR` 是否一致。

## 5. 可选与兼容配置

| 环境变量 | 用途 |
| --- | --- |
| `CANTEEN_SCANNER_BAUD` | 串口波特率，默认 `115200`；允许 9600、19200、38400、57600、115200、230400 |
| `CANTEEN_SCANNER_TERMINATOR` | 帧结束字符，`any`（默认）、`cr`、`lf`、`none` |
| `CANTEEN_SCANNER_IDLE_MS` | 无后缀时的帧结束静默阈值，默认 `50`，取值 10–500 |
| `CANTEEN_DISPLAY_ADDR` | 本地页面监听，默认 `127.0.0.1:8090` |
| `CANTEEN_TTS_COMMAND` | 语音程序的绝对路径，可不配置 |
| `CANTEEN_TERMINAL_CREDENTIAL_FILE` | 未设置新 key 变量时，从此文件读取 key |
| `CANTEEN_INTERNAL_URL` | 未设置统一地址时，内部 API 地址，默认 `http://127.0.0.1:8081` |
| `CANTEEN_PUBLIC_URL` | 未设置统一地址时，Web 地址，默认 `http://127.0.0.1:5001` |

`CANTEEN_SERVER_URL` 优先于两个旧地址；`CANTEEN_TERMINAL_KEY` 优先于凭据文件。新变量设置为空或非法值时会报错，不会静默使用旧配置。统一地址可包含反向代理前缀，例如 `https://example.com/canteen`；该代理须将此前缀映射到服务端根路径。

数据位、校验位和停止位固定为 8N1，暂不支持 7E1 等组合；确需这类扫码枪时另行提出。

## 6. 从旧配置升级

`CANTEEN_SCANNER_MODE` 已移除，配置中保留该变量的非空值时启动会直接报错并提示删除，不会静默降级。旧配置使用的 `/dev/input/eventX`、`/dev/hidrawX` 等输入设备路径不再可用，须改为串口路径。

Agent 不再支持以键盘类设备方式读取的扫码枪。这类扫码枪需要改为串口接入，或作为新的设备模式另行评估。

Agent 启动即发送心跳，之后每 10 秒发送；待确认消费每 2 秒查询一次结果，重启后恢复查询。网络失败时显示离线，不缓存扫码后补扣；请求超时不代表服务端一定未处理，不能自行假定失败后重复扣款。

旧终端只有凭据哈希，升级后原 key 仍可用，后台不能还原其原文；可继续使用原文件，或在后台重置取得新 key。CLI `provision-terminal` 保留为备用，新创建凭据也可从后台取回。

自制客户端与 ESP32 调用方式见 [终端 API](../docs/terminal-api.md)。
