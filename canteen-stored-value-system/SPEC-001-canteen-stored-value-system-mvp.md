# SPEC-001：食堂储值卡消费系统 MVP

- 状态：Ready for Development
- 日期：2026-09-26
- 依赖：ADR-001

# 1. 目标

实现一套可真实投入内部食堂使用的储值卡消费系统，覆盖：

```text
人员建档
→ 充值
→ 员工登录
→ 动态二维码
→ 扫码枪扫码
→ 自动扣款
→ 结果反馈
→ 流水查询
→ 退款/冲正
→ 日结
→ 备份
```

# 2. MVP 范围

## 2.1 包含

- 管理员账号
- 员工账号
- 员工档案
- 单余额账户
- 充值
- 充值冲正
- 消费
- 全额退款
- 余额调整
- 动态二维码
- Linux Scan Agent
- 餐次配置
- 重复消费提醒
- 终端状态
- 语音反馈
- Excel 导入导出
- 扫码事件日志
- 审计日志
- 日结
- 自动备份

## 2.2 不包含

- 在线支付
- 短信验证码
- 多食堂
- 多租户
- 复杂 RBAC
- 双钱包
- 部分退款
- IC/NFC 卡
- 防截图/近场认证
- 第三方业务 API
- 小票打印

# 3. 用户角色

## 3.1 管理员

可执行：

- 登录管理后台
- 管理员工
- 导入/导出员工
- 冻结/解冻账户
- 充值
- 充值冲正
- 退款
- 余额调整
- 查看流水
- 配置餐次
- 查看终端状态
- 查看事件日志
- 查看审计日志
- 查看日结
- 立即备份

## 3.2 普通员工

可执行：

- 手机号 + 密码登录
- 查看动态消费二维码
- 查看当前余额
- 查看消费记录
- 查看个人基础信息
- 退出登录

# 4. 员工与账户

员工字段：

```text
id
employee_no
name
phone
department
id_card
position
photo
hire_date
leave_date
status
created_at
updated_at
```

`employee_no` 唯一。

`phone` 作为登录账号，MVP 要求唯一。

员工状态：

```text
ACTIVE
FROZEN
CANCELLED
```

账户字段：

```text
id
user_id
balance
status
created_at
updated_at
```

账户余额单位为“分”。

账户状态至少：

```text
ACTIVE
FROZEN
CLOSED
```

冻结账户不得消费。

# 5. 认证与会话

员工使用：

```text
手机号 + 密码
```

登录会话默认保持 30 天。

不强制首次修改密码。

忘记密码由管理员重置。

一个员工仅允许一个有效设备会话。

新设备登录成功后：

1. 旧会话失效；
2. 旧会话关联的所有未过期支付 Token 立即失效。

管理员登录采用独立管理员账号。

密码必须使用安全密码哈希算法存储，禁止明文保存。

# 6. 动态二维码协议

## 6.1 Token

二维码只包含随机 Token，例如：

```text
pmt_xxxxxxxxxxxxxxxxxxxxxxxx
```

禁止编码：

- user_id
- employee_no
- phone
- name

Token 使用密码学安全随机数，随机强度不得低于 128 bit。

数据库保存 `token_hash`，不保存原始 Token。

## 6.2 刷新

H5 打开二维码页面后，每 30 秒请求一个新 Token。

每个用户同一时间最多保留：

- 当前 Token
- 上一周期 Token

有效窗口约 60 秒。

## 6.3 Token 状态

建议：

```text
ACTIVE
PROCESSED
EXPIRED
REVOKED
```

登出、被顶下线、账户注销等情况应撤销关联 Token。

冻结账户时，即使 Token 仍有效，也不得消费。

# 7. 扫码链路

Scan Agent 从扫码设备读取字符串后调用后端内部消费接口。

Scan Agent 不直接访问 SQLite。

流程：

```text
读取扫码数据
→ 本地格式校验
→ 防抖
→ 调用消费接口
→ 接收结果
→ 更新状态屏/语音
```

建议优先采购支持 USB 虚拟串口的扫码枪。

若使用 HID，Scan Agent 必须直接读取 Linux input device，不依赖浏览器输入框。

# 8. 餐次

默认三餐：

```text
早餐
午餐
晚餐
```

字段：

```text
id
name
start_time
end_time
amount
enabled
created_at
updated_at
```

规则：

- 时间禁止重叠；
- 金额单位为分；
- 当前无有效餐次时拒绝消费；
- 修改餐费立即对新交易生效；
- 已完成历史交易金额不得变化。

# 9. 消费状态机

```text
TOKEN_RECEIVED
    ↓
TOKEN_VALID?
 ├─ No → REJECT
 └─ Yes
    ↓
SAME_TOKEN_ALREADY_PROCESSED?
 ├─ Yes → RETURN_PREVIOUS_RESULT
 └─ No
    ↓
SESSION_VALID?
 ├─ No → REJECT
 └─ Yes
    ↓
USER_AND_ACCOUNT_ACTIVE?
 ├─ No → REJECT
 └─ Yes
    ↓
CURRENT_MEAL_EXISTS?
 ├─ No → REJECT
 └─ Yes
    ↓
ALREADY_CONSUMED_THIS_MEAL?
 ├─ Yes → WAIT_CONFIRM
 └─ No
    ↓
BALANCE_ENOUGH?
 ├─ No → REJECT
 └─ Yes
    ↓
ATOMIC_CONSUME
    ↓
SUCCESS
```

# 10. 幂等

必须区分：

## 10.1 同 Token 重放

同一个 Token 重复提交时：

- 不再次扣款；
- 返回首次消费结果或首次终态结果；
- 必须由后端和数据库约束保证。

## 10.2 同餐次重复

新 Token 在同一餐次再次消费：

- 返回 `DUPLICATE_MEAL_CONFIRM_REQUIRED`；
- 终端显示确认界面；
- 待确认记录有效约 30 秒；
- 用户点击继续后直接扣款；
- 不要求再次扫码。

# 11. 资金交易

交易表建议：

```text
id
transaction_no
account_id
user_id
type
amount
before_balance
after_balance
meal_period_id
terminal_id
reference_transaction_id
operator_id
reason
idempotency_key
created_at
```

交易类型：

```text
RECHARGE
RECHARGE_REVERSAL
CONSUME
REFUND
BALANCE_ADJUSTMENT
```

所有交易禁止物理删除。

# 12. 原子扣款

消费必须在单事务中完成。

伪逻辑：

```text
BEGIN IMMEDIATE

校验幂等键
读取账户
校验账户状态
校验余额
写消费流水
更新账户余额
标记 Token 处理结果

COMMIT
```

失败：

```text
ROLLBACK
```

必须保证不会出现：

- 扣余额成功但无流水；
- 有流水但余额未扣；
- 同一 Token 产生两笔消费。

# 13. 充值

管理员输入充值金额。

支持：

- 固定金额快捷选择
- 任意合法金额

充值产生 `RECHARGE` 流水。

充值后立即更新余额。

充值错误不得删除或修改原流水。

# 14. 充值冲正

充值冲正：

- 必须关联原充值；
- 产生 `RECHARGE_REVERSAL`；
- 冲正金额等于原充值金额；
- 若冲正后会导致余额 < 0，则拒绝。

同一充值最多成功冲正一次。

# 15. 退款

消费退款：

- 必须关联原 `CONSUME`；
- MVP 仅支持全额退款；
- 产生 `REFUND`；
- 每笔消费最多成功退款一次；
- 原消费永久保留。

终端“撤销上一笔”和后台退款复用同一服务。

终端撤销限制最近一笔且约 5 分钟内。

后台管理员允许对历史未退款消费执行退款。

# 16. 余额调整

管理员可执行：

```text
正向调整
负向调整
```

必须填写原因。

负向调整不得使余额为负。

生成 `BALANCE_ADJUSTMENT` 流水。

# 17. 消费结果

成功结果至少包含：

```text
transaction_no
user_name
photo_url
meal_name
amount
balance
consumed_at
terminal_id
```

终端显示约 3 秒：

- 照片
- 姓名
- 本次金额
- 剩余余额
- 时间

成功语音：

```text
支付成功，余额 XX 元
```

外设显示/语音失败不回滚资金交易。

# 18. 失败结果

至少覆盖：

```text
INVALID_TOKEN
EXPIRED_TOKEN
INVALID_SESSION
USER_FROZEN
ACCOUNT_FROZEN
NO_ACTIVE_MEAL
INSUFFICIENT_BALANCE
DUPLICATE_MEAL_CONFIRM_REQUIRED
PENDING_CONFIRM_EXPIRED
TERMINAL_DISABLED
INTERNAL_ERROR
```

资金未变化的失败请求不进入资金流水。

应写入扫码事件日志。

# 19. 终端

字段建议：

```text
id
code
name
status
device_path
last_heartbeat_at
last_scan_at
last_success_transaction_at
created_at
updated_at
```

状态至少：

```text
ONLINE
OFFLINE
DISABLED
```

管理后台显示：

- Scan Agent 在线
- 扫码枪在线
- 后端状态
- 数据库状态
- 语音模块状态
- 最近扫码
- 最近成功消费

# 20. 扫码事件日志

建议字段：

```text
id
terminal_id
user_id
token_fingerprint
event_type
message
payload_summary
created_at
```

事件包括：

```text
TOKEN_INVALID
TOKEN_EXPIRED
ACCOUNT_FROZEN
BALANCE_INSUFFICIENT
DUPLICATE_SCAN
DUPLICATE_MEAL
DEVICE_OFFLINE
```

不得记录完整原始 Token。

# 21. 审计日志

管理员重要操作必须记录：

```text
ADMIN_LOGIN
USER_CREATE
USER_UPDATE
USER_FREEZE
USER_UNFREEZE
RECHARGE
RECHARGE_REVERSAL
REFUND
BALANCE_ADJUSTMENT
MEAL_CONFIG_UPDATE
SYSTEM_CONFIG_UPDATE
MANUAL_BACKUP
```

审计日志 MVP 不提供删除入口。

# 22. Excel 导入

流程：

```text
上传
→ 解析
→ 校验
→ 预览
→ 确认导入
```

规则：

- 支持重复工号处理策略；
- 正确数据可以导入；
- 错误数据不阻塞全部数据；
- 返回错误行号和错误原因；
- 可下载错误报告。

# 23. Excel 导出

支持：

- 人员
- 账户余额
- 充值流水
- 消费流水
- 退款流水
- 调整流水
- 日结结果

# 24. 日结

每天 00:00 生成日结。

字段建议：

```text
id
settlement_date
opening_balance
recharge_amount
recharge_reversal_amount
consume_amount
refund_amount
adjustment_amount
expected_closing_balance
actual_closing_balance
difference
status
created_at
```

规则：

```text
expected_closing_balance
=
opening_balance
+ recharge
- recharge_reversal
- consume
+ refund
+ adjustment
```

然后比较：

```text
expected_closing_balance == actual_closing_balance
```

不一致：

```text
status = MISMATCH
```

必须报警，不得自动修正账户余额。

# 25. 首页仪表盘

显示：

- 总人数
- 正常账户数
- 冻结账户数
- 总余额
- 今日早餐消费人数
- 今日午餐消费人数
- 今日晚餐消费人数
- 今日消费金额
- 今日充值金额
- 今日异常扫码数
- 终端状态

# 26. 系统配置

可配置业务参数：

- 餐次
- 餐费
- 防抖秒数
- 重复消费确认超时
- 备份目录
- 备份保留天数

二维码随机强度、密码哈希算法等安全参数不允许普通管理员随意修改。

# 27. SQLite

必须启用 WAL。

建议启动时设置：

```sql
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;
```

所有写操作由 Go Server 完成。

Scan Agent 禁止直接写 SQLite。

# 28. 备份

每天自动备份。

默认保留 7 天。

至少支持一个外部备份路径：

- U盘
- NAS
- 挂载目录

后台可触发立即备份。

备份必须使用 SQLite 官方安全备份方式或一致性快照方式，禁止在 WAL 活跃时直接粗暴复制数据库文件。

恢复只允许命令行执行。

# 29. systemd

服务：

```text
canteen-server.service
scan-agent.service
```

要求：

- `Restart=on-failure`
- 开机自启动
- 日志进入 journald 或统一日志目录
- 后端未启动时 Scan Agent 持续重连
- 扫码设备断开后自动重新发现

# 30. 建议 API

## 员工端

```text
POST /api/auth/login
POST /api/auth/logout
GET  /api/me
GET  /api/me/account
GET  /api/me/transactions
POST /api/me/payment-token
```

## 终端内部 API

```text
POST /api/internal/terminals/{terminalId}/heartbeat
POST /api/internal/consume
POST /api/internal/consume/{pendingId}/confirm
POST /api/internal/transactions/{id}/refund
```

终端 API 必须进行设备级认证，不得裸开放公网调用。

## 管理端

```text
POST /api/admin/login

GET/POST/PUT /api/admin/users
POST /api/admin/users/import
GET  /api/admin/users/export

POST /api/admin/accounts/{id}/recharge
POST /api/admin/transactions/{id}/reverse
POST /api/admin/transactions/{id}/refund
POST /api/admin/accounts/{id}/adjust

GET/PUT /api/admin/meal-periods
GET     /api/admin/transactions
GET     /api/admin/scan-events
GET     /api/admin/audit-logs
GET     /api/admin/terminals
GET     /api/admin/settlements

POST /api/admin/backups
```

# 31. 数据库约束

至少建立：

- `users.employee_no UNIQUE`
- `users.phone UNIQUE`
- `accounts.user_id UNIQUE`
- `transactions.transaction_no UNIQUE`
- `transactions.idempotency_key UNIQUE`（对有幂等需求的交易）
- Token Hash 唯一约束或满足等价幂等能力
- 退款/冲正唯一业务约束，防止重复退款和重复冲正

# 32. 安全要求

- 全公网访问必须 HTTPS。
- SQLite 不监听公网。
- 员工和管理员密码必须安全哈希。
- 日志不得写密码、完整 Token、身份证完整值。
- 管理后台接口必须鉴权。
- 内部终端 API 使用独立 terminal credential。
- 文件上传限制类型和大小。
- Excel 导入必须防止公式注入风险。
- 所有账务接口由服务端计算余额，不接受客户端提交 `after_balance`。

# 33. 非功能要求

## 33.1 性能

目标规模下：

- 普通消费接口 P95 < 300ms（不含外部网络异常）
- 本地扫码到结果反馈目标 < 1s
- 1～3 终端并发消费无重复扣款

## 33.2 可用性

- 工控机重启后自动恢复
- 扫码枪重新插入后无需重启整机
- 后端短暂重启后 Scan Agent 自动重连

## 33.3 可维护性

- 数据库 schema 使用 migration 管理
- 配置集中管理
- 日志结构化
- 所有资金业务必须有自动化测试

# 34. MVP 验收场景

必须全部通过：

1. 管理员创建员工。
2. 管理员充值 100 元。
3. 员工登录 H5。
4. H5 每 30 秒刷新二维码。
5. 扫码后自动识别当前餐次。
6. 正常扣款成功。
7. 页面显示员工照片、金额、余额。
8. 语音播报成功。
9. 同一 Token 连扫两次只扣一次。
10. 同餐次使用新 Token 再次扫码进入确认。
11. 确认后完成第二次扣款。
12. 余额不足时拒绝。
13. 冻结账户拒绝消费。
14. 过期 Token 拒绝。
15. 管理员退款后产生反向流水。
16. 原消费仍可查询。
17. 充值冲正成功。
18. 负向余额调整不能产生负余额。
19. Excel 可批量导入人员并输出错误报告。
20. 日结正确。
21. 人为构造账务差异时日结产生告警。
22. 工控机重启后服务自动恢复。
23. 扫码枪拔出后状态页显示离线。
24. 重新插入扫码枪后自动恢复。
25. 自动备份成功并执行 7 天保留策略。

# 35. Definition of Done

MVP 只有在以下条件全部满足后才算完成：

- 所有核心主链路验收通过；
- 账务自动化测试通过；
- 幂等并发测试通过；
- SQLite 备份/恢复演练通过；
- Linux 工控机断电重启演练通过；
- Scan Agent 设备断连恢复演练通过；
- 管理员与员工关键页面可用；
- 无已知资金重复扣款缺陷；
- 无允许直接改余额的旁路接口。
