# 食堂储值卡 Web

React、Vite、TypeScript 实现的员工 H5、管理员后台和独立核销屏。账户、余额、流水、终端结果等业务数据均来自服务 API；页面不包含演示账户或演示交易。视觉沿用 `design/reference/` 与 `assets/ui/`。

## 本地运行

先在仓库根目录启动 Go 公共服务：

```sh
task dev:backend
```

另开终端运行 `task dev:web` 并打开 Vite 打印的地址。开发服务器将 `/api` 和 `/healthz` 代理到 Go 公共服务。若公共服务改用其他端口，可在启动 Vite 时设置 `CANTEEN_WEB_API_TARGET`，例如 `CANTEEN_WEB_API_TARGET=http://127.0.0.1:8090 task dev:web`。

单独编译前端可运行 `task web:build`。生产构建运行 `task build`；这会将 Vite 产物嵌入 Go Server 二进制。使用 `task run` 构建并运行服务，页面与 `/api` 由同一公开监听提供。员工会话仅保存在浏览器当前标签页的 `sessionStorage` 中；退出时会请求撤销服务端会话并清除本地会话。就餐码仅在就餐码页面内存中保留，页面离开后销毁。

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

金额按后端返回的分显示为元。首页餐次卡片直接展示已配置的时段、价格和开放状态。动态码按服务端 `refresh_after` 更新，并在 `expires_at` 到达后隐藏。
