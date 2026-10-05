# 扫码终端 API

电脑、ESP32 或其他能发送 HTTP 请求的设备都可以作为扫码终端。后台创建终端，取得独立 key；服务端基础地址支持 HTTP、HTTPS，可与扫码设备分机部署。所有终端请求使用同一个基础地址，不需要配置内部监听端口。

## 最小接入：提交扫码

扫码枪读出的内容是完整 `pmt_...` 就餐码，不是 URL。把它原样放入 `token`，携带终端 key：

```sh
SERVER_URL='http://192.168.1.10:5001'
TERMINAL_KEY='trm_替换为后台复制的完整key'
TOKEN='pmt_替换为本次扫码内容'

curl --fail-with-body -X POST "$SERVER_URL/api/v1/terminal/scan" \
  -H "Authorization: Bearer $TERMINAL_KEY" \
  -H 'Content-Type: application/json' \
  --data "{\"token\":\"$TOKEN\"}"
```

终端身份由 key 确定，不提交员工编号、终端编号或扣款金额。服务端负责餐次、余额、去重、扣款与资金流水。

HTTP 200 表示已得到业务结果，不能仅凭 HTTP 状态提示消费成功。结果示例：

```json
{"status":"SUCCESS","code":"CONSUMED","message":"消费成功","amount_cents":1200,"transaction_id":123}
```

| 响应 | 客户端处理 |
| --- | --- |
| `status=SUCCESS` | 显示消费成功；`amount_cents` 单位为分 |
| `status=FAILED` | 显示 `message`；如余额不足、码失效、无餐次或消费入口关闭 |
| `status=PENDING` | 提示员工在手机确认，保存 `pending_id`；这时尚未完成本次扣款 |
| `replayed=true` 或 `duplicate_trigger=true` | 返回已有结果，不按一笔新消费重复播报或打印 |
| HTTP 401 | key 错误、已重置或终端停用，检查后台与设备配置 |
| HTTP 400 / 405 | 请求字段或方法错误，修正客户端请求 |
| HTTP 503 / 网络异常 | 不显示成功，不排队离线补扣；超时可能发生在服务端处理之后，不能据此断言没有扣款 |

只实现扫码即可接入。不发送心跳时，后台显示“未报告心跳”，不影响扫码；不实现待确认查询时，设备提示“请在手机确认”，员工在手机查看最终结果。

## 完整流程：心跳与待确认结果

1. 客户端启动后发送心跳；官方 Linux Agent 后续每 10 秒发送一次。
2. 读取就餐码，调用扫码接口，处理成功、失败、重复触发或待确认。
3. 收到待确认后由员工本人在手机确认；终端只负责显示和查询，没有确认扣款接口。
4. 根据 `pending_id` 查询结果；官方 Agent 每 2 秒查询，直到 `status` 不再是 `PENDING`，显示最终结果。
5. 重启后通过 recent 接口恢复未结束的请求；网络失败时保留查询目标，恢复连接后继续查询，不缓存扫码后自动补扣。

```sh
# 可选心跳。无语音模块使用 UNAVAILABLE。
curl --fail-with-body -X POST "$SERVER_URL/api/v1/terminal/heartbeat" \
  -H "Authorization: Bearer $TERMINAL_KEY" \
  -H 'Content-Type: application/json' \
  --data '{"scanner_status":"READY","voice_status":"UNAVAILABLE"}'

# pending_id 来自扫码返回结果
PENDING_ID='替换为pending_id'
curl --fail-with-body "$SERVER_URL/api/v1/terminal/pending/$PENDING_ID" \
  -H "Authorization: Bearer $TERMINAL_KEY"

# 启动恢复：items 含未结束请求以及最近两分钟的请求结果
curl --fail-with-body "$SERVER_URL/api/v1/terminal/pending/recent" \
  -H "Authorization: Bearer $TERMINAL_KEY"
```

待确认请求由服务端管理确认期限。取消、失效或业务失败返回失败结果及说明；合法格式但未知或不属于本终端的请求返回 HTTP 404，非法编号格式返回 HTTP 400。

| 接口 | 响应 |
| --- | --- |
| `POST /api/v1/terminal/scan` | 单个消费结果 |
| `POST /api/v1/terminal/heartbeat` | `{"status":"ok"}` |
| `GET /api/v1/terminal/pending/{pending_id}` | 对应请求的当前结果 |
| `GET /api/v1/terminal/pending` | 最近两分钟内最新请求的结果，无请求时 404 |
| `GET /api/v1/terminal/pending/recent` | `{"items":[...]}`，用于启动恢复 |
| `GET /api/v1/terminal/events?after_id=0` | `{"items":[...]}`，本终端脱敏扫码事件 |

所有接口都需要 key；它不能用于管理员接口或员工确认接口。后台重置 key 后更新设备配置并重启；停用后所有受保护终端接口拒绝请求，历史记录保留。

## ESP32 示例

见 [Arduino ESP32 调用示例](../examples/esp32-terminal/esp32-terminal.ino)。在 Arduino ESP32 开发环境中配置 Wi-Fi、服务端地址和终端 key，串口输入一行真实就餐码即可提交请求。示例输出 HTTP 状态和 JSON 响应；它只展示协议接入，不包含扫码硬件驱动、显示屏或完整固件流程。

HTTP 使用普通网络客户端；HTTPS 使用安全网络客户端并填写服务器信任的 CA 证书。示例按 Espressif 官方 [HTTPClient 接口](https://github.com/espressif/arduino-esp32/blob/master/libraries/HTTPClient/src/HTTPClient.h) 编写，未在真实 ESP32 上编译或烧录验证。设备展示结果时须按上面的业务状态规则解析 JSON，不能把 HTTP 200 当作成功。

Linux 官方 Agent 的最简启动方式见 [部署文档](../deploy/scan-agent.md)。
