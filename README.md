# canteen-wallet

公司内部单食堂的员工储值餐费系统。员工通过手机 H5 展示动态就餐码，现场终端扫码后按餐次扣款；管理员处理线下充值、退款、结清与对账。

**实施状态：**[SPEC-001](https://github.com/EziosWJ/canteen-wallet/issues/1) 与 CW-01 至 CW-19 任务已经建立；CW-01 至 CW-07 的后端基础与账务接口、CW-12 静态页面稿、CW-13 员工 H5 已落地。终端消费、管理后台、运营和现场验收仍按后续任务推进。编译通过不代表业务或现场验收通过。

## 实施基线

- [SPEC-001](https://github.com/EziosWJ/canteen-wallet/issues/1) 是产品和技术要求的实施规格；[CONTEXT.md](./CONTEXT.md) 定义领域术语，[ADR-0001](./docs/adr/0001-mvp-architecture-and-money-model.md) 记录架构与资金模型。
- [assets/ui/](./assets/ui/README.md) 保存用户提供的八张素材；[design/reference/](./design/reference/README.md) 保存登录、员工首页、核销页面视觉稿。页面稿及正式实现沿用其布局、层级和蓝绿视觉语言。
- [web/](./web/README.md) 是 React、Vite、TypeScript 员工端，调用同源真实 API。CW-12 的静态交互页面稿仅用于审阅、含 Mock 数据，未纳入版本库。
- `canteen-stored-value-system/` 和 `frontpage/` 是历史资料。新任务不依赖它们；正式视觉输入已经保存于上述目录。

## 构建

需要 [Task](https://taskfile.dev/docs/installation)、Go 1.26、Node.js 与 npm。所有编译和运行入口由根目录的 [Taskfile.yml](./Taskfile.yml) 管理：

```sh
task --list
task build
task run
```

`task build` 执行前端依赖安装与 TypeScript/Vite 生产编译，将 `web/dist` 复制到 `backend/internal/webassets/dist`，再生成 `build/canteen-server`。`task run` 会先完整构建，再前台启动该二进制。二进制在公开监听上提供嵌入的员工页面和同源 `/api`；内部终端 API 仍由独立监听提供。`web/dist`、嵌入副本和最终二进制均为生成物，不提交到 Git。`task backend:compile` 可单独编译检查所有 Go 包；`task dev:backend` 与 `task dev:web` 分别启动本地后端和 Vite 开发服务器，需在两个终端运行。

Vite 将 `/api` 代理到 `127.0.0.1:8080`；`CANTEEN_WEB_API_TARGET` 可改代理目标。默认公开 HTTP 监听 `127.0.0.1:8080`，内部终端 HTTP 监听 `127.0.0.1:8081`，数据库位于 `backend/data/canteen.db`。正式环境由 HTTPS 反向代理暴露公开入口，内部终端入口仅供现场设备访问。

`CANTEEN_PUBLIC_ADDR`、`CANTEEN_INTERNAL_ADDR`、`CANTEEN_DB_PATH`、`CANTEEN_TIME_ZONE` 分别配置监听地址、数据库路径和餐次时区（默认 `Asia/Shanghai`）。服务启动时自动创建 SQLite 数据库，启用 WAL、外键和忙等待并执行尚未应用的迁移；已应用迁移不可修改。

## 已有接口与约束

首次管理员在 `backend/` 运行 `go run ./cmd/server create-admin <username>` 建立。密码由终端隐藏输入并确认，不作为命令参数。管理员通过 `POST /api/admin/login` 获取 12 小时 Bearer 会话；真实试点前仍需按 CW-15 接入独立第二因素。公开入口还需在反向代理限制登录请求速率。

管理员可通过 `/api/admin/employees` 建档和查询，通过 `/api/admin/employees/{id}` 查看、调整状态及重置临时密码；临时密码只返回一次。`POST /api/admin/recharges` 确认带凭据的线下充值，`POST /api/admin/recharges/{id}/reverse` 冲正。`POST /api/admin/accounts/{id}/adjust` 接收带符号的 `amount_cents`、`reason`、`idempotency_key`；`POST /api/admin/accounts/{id}/withdraw` 接收 `payout_ref`、`paid_at`、`payment_method`、`idempotency_key`，关户后的历史退款结清可另带 `related_refund_transaction_id`。零余额直接关户仍使用员工状态接口。资金操作均由后端写入流水和审计，不能直接改余额。

员工通过 `POST /api/auth/login` 登录，首次使用临时密码必须通过 `POST /api/me/change-password` 修改。`GET /api/me`、`GET /api/me/account`、`GET /api/me/transactions`、`GET /api/me/meal-periods`、`POST /api/me/payment-token` 和 `POST /api/auth/logout` 均需员工 Bearer 会话。本人流水按 `?cursor=<上页 next_cursor>&limit=20` 游标分页，返回 `items` 和 `next_cursor`；新设备登录撤销旧会话。动态就餐码约每 30 秒允许刷新一次，提前请求返回 429 和下次可刷新时间；目前仍需后续消费链路才能完成扫码扣款。

`GET /api/admin/meal-periods` 和 `PUT /api/admin/meal-periods/{code}` 用于配置三餐时段及固定餐费。接口和任务边界详见 SPEC-001 与各 GitHub Issue。当前阶段只要求前后端编译；浏览器和现场业务流程由用户人工测试。
