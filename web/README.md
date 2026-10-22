# 食堂储值卡 Web

React、Vite、TypeScript 实现的员工 H5、管理员后台和独立核销屏。账户、余额、流水、终端结果等业务数据均来自服务 API；页面不包含演示账户或演示交易。视觉沿用 `design/reference/` 与 `assets/ui/`。

## 本地运行

先在仓库根目录启动 Go 公共服务：

```sh
task dev:backend
```

另开终端运行 `task dev:web` 并打开 Vite 打印的地址。开发服务器将 `/api` 和 `/healthz` 代理到 Go 公共服务。若公共服务改用其他端口，可在启动 Vite 时设置 `CANTEEN_WEB_API_TARGET`，例如 `CANTEEN_WEB_API_TARGET=http://127.0.0.1:8090 task dev:web`。

单独编译前端可运行 `task web:build`。生产构建运行 `task build`；这会将 Vite 产物嵌入 Go Server 二进制。使用 `task run` 构建并运行服务，页面与 `/api` 由同一公开监听提供。员工会话保存在当前站点的 `localStorage` 中，因此固定自助消费链接在同一浏览器的新标签页可以复用会话；登录、退出、会话替换和过期会通过浏览器存储事件同步到其他标签页。就餐码缓存和自助消费结果仍按各自流程保存，退出时会清除关联的本地数据。

## 页面与接口

| 页面 | 接口 |
| --- | --- |
| 登录、退出 | `POST /api/auth/login`、`POST /api/auth/logout` |
| 首次及日常修改密码 | `POST /api/me/change-password` |
| 首页、个人信息 | `GET /api/me`、`GET /api/me/account` |
| 首页餐次卡片 | `GET /api/me/meal-periods` |
| 动态就餐码 | `POST /api/me/payment-token` |
| 首页最近流水、全部流水 | `GET /api/me/transactions?cursor=...` |
| 管理员登录、退出 | `POST /api/admin/login`、`GET /api/admin/me`、`POST /api/admin/logout` |
| 人员、状态和临时密码 | `/api/admin/employees`、`/api/admin/employees/{id}/status`、`/reset-password` |
| 充值、冲正、退款、调整、余额退还 | `/api/admin/recharges`、`/api/admin/recharges/{id}/reverse`、`/api/admin/transactions/{id}/refund`、`/api/admin/accounts/{id}/adjust|withdraw` |
| 餐次、流水、终端和扫码事件 | `/api/admin/meal-periods`、`/api/admin/transactions`、`/api/admin/terminals`、`/api/admin/scan-events` |
| 日结、收款复核 | `/api/admin/reconciliation/daily`、`/api/admin/receipt-reviews` |
| Excel、故障供餐补录、备份和导出 | `/api/admin/imports`、`/api/admin/manual-supplies`、`/api/admin/backups`、`/api/admin/exports/{kind}` |
| 核销屏 | 本地 Scan Agent 的 `GET /api/display/state`、`POST /api/display/confirm` |

管理页面在 `/admin`，核销屏在 `/terminal`。管理员使用独立 Bearer 会话并输入管理员第二因素验证码。核销浏览器只访问本机 Scan Agent 的只读状态与人工确认代理；设备凭据不进入浏览器存储。

系统管理菜单提供自助消费二维码（`/admin/self-service-qr`）和就餐码消费二维码（`/admin/payment-code-qr`），均支持修改完整地址、复制链接、下载及打印。就餐码二维码编码固定入口 `/payment-code`；员工扫码后直接进入“我的就餐码”，未登录或需修改临时密码时，完成后仍进入该页面。该入口二维码与员工用于终端核销的动态就餐码不同。

员工页使用浏览器历史管理导航：首页为 `/`，就餐码为 `/payment-code`，资金流水为 `/transactions`，个人信息为 `/profile`，自助消费为 `/self-service`。扫码直达的消费页也保留一个首页历史记录；返回键先回首页，首页再次返回可离开网页。底部菜单切换共用一个子页面记录；修改密码和充值说明各自增加一层，返回时先关闭。登录和首次改密在目标地址内完成。刷新不会重复增加历史记录。

页面从历史缓存恢复、切回前台时，会重新读取本地员工会话并校验服务端状态；暂时断网不会清除登录，旧会话的迟到响应也不会注销新会话。就餐码恢复时会立即检查有效期并同步扫码结果。登录有效期和新登录撤销旧会话的规则沿用后端配置。

金额按后端返回的分显示为元。首页餐次卡片直接展示已配置的时段、价格和开放状态。动态码按服务端 `refresh_after` 更新，并在 `expires_at` 到达后隐藏。

## 返回导航回归检查

启动 Vite（默认测试地址 `http://127.0.0.1:4187`）和一个独立测试 Chromium（开启 `--remote-debugging-port=9237`，使用独立的 `--user-data-dir`），再执行 `npm --prefix web run test:navigation`。可用 `CANTEEN_TEST_WEB_URL` 和 `CANTEEN_TEST_CDP_URL` 覆盖地址。脚本使用模拟接口和测试会话，会清理测试浏览器中当前站点的本地存储，勿连接日常使用的浏览器。

覆盖缓存登录页恢复、扫码后返回、首页离开、切页与刷新、前进后退、改密与说明弹窗、自助消费入口、断网、会话失效和迟到响应。浏览器回归不替代 Android/iOS 微信真机对返回按钮、手势和网页关闭行为的验证。
