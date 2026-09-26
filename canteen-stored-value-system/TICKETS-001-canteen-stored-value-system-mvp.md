# TICKETS-001：食堂储值卡消费系统 MVP 开发任务

- 日期：2026-09-26
- 状态：Ready
- 依赖：ADR-001、SPEC-001
- 建议执行方式：按 Epic 顺序推进，标记可并行任务

# Epic 0：工程基线

## TICKET-001 初始化 Go + React 单仓库

目标：建立可持续开发的工程骨架。

范围：

- Go Server
- React Admin
- React H5
- Scan Agent
- migration
- 基础配置
- Makefile/Taskfile

建议结构：

```text
cmd/server
cmd/scan-agent
internal/
web/admin
web/h5
migrations/
deploy/systemd/
scripts/
```

验收：

- Go 服务可启动；
- Admin/H5 可构建；
- Scan Agent 可独立启动；
- 开发环境一条命令启动。

依赖：无。

---

## TICKET-002 SQLite 初始化与 Migration

目标：建立 SQLite WAL 数据库基线。

范围：

- WAL
- foreign_keys
- busy_timeout
- migration runner
- 初始化 schema

验收：

- 首次启动自动建库；
- 重复启动不重复执行 migration；
- WAL 正常生效；
- migration 可升级。

依赖：TICKET-001。

---

## TICKET-003 基础日志、配置和错误模型

目标：统一后端、Scan Agent 的运行规范。

范围：

- 结构化日志
- 配置文件/环境变量
- request_id
- error_code
- 禁止敏感字段进入日志

验收：

- API 错误具有固定 code；
- 日志可关联单次请求；
- Token/密码不会被完整打印。

依赖：TICKET-001。

# Epic 1：身份与人员

## TICKET-004 管理员认证

范围：

- 管理员表
- 密码哈希
- 登录/退出
- 管理端鉴权 middleware
- 审计登录事件

验收：

- 多管理员可登录；
- 错误密码不可登录；
- 密码不明文保存；
- 未认证无法访问管理接口。

依赖：TICKET-002、003。

---

## TICKET-005 员工 CRUD 与账户自动创建

范围：

- 员工新增/修改/查询
- 工号唯一
- 手机号唯一
- 创建员工时创建账户
- 冻结/解冻
- 注销状态

验收：

- 新员工默认余额 0；
- 冻结后消费被拒绝；
- 关键操作生成审计日志。

依赖：TICKET-004。

---

## TICKET-006 员工登录与单设备会话

范围：

- 手机号 + 密码
- 30 天会话
- 单设备在线
- 新登录顶掉旧设备
- 管理员重置员工密码
- 退出登录

验收：

- 新设备登录后旧会话立即无效；
- 退出后会话无效；
- 被顶下线关联 Token 可被撤销。

依赖：TICKET-005。

# Epic 2：账务核心

## TICKET-007 账户与交易账本

范围：

- accounts
- transactions
- 金额统一 int64 分
- before/amount/after
- transaction_no
- 基础查询

验收：

- 禁止 float 金额；
- 资金流水不可删除；
- 所有余额变化必须通过账务服务。

依赖：TICKET-002、005。

---

## TICKET-008 充值

范围：

- 管理员充值
- 固定金额/任意金额
- 原子更新余额
- `RECHARGE`
- 审计日志

验收：

- 充值与余额更新原子完成；
- 并发充值余额正确。

依赖：TICKET-007。

---

## TICKET-009 充值冲正

范围：

- 关联原充值
- `RECHARGE_REVERSAL`
- 防重复冲正
- 余额不足禁止冲正

验收：

- 原充值流水保留；
- 同一充值不能冲正两次；
- 冲正后余额正确。

依赖：TICKET-008。

---

## TICKET-010 余额调整

范围：

- 正向/负向
- 强制原因
- 不允许负余额
- `BALANCE_ADJUSTMENT`

验收：

- 无原因拒绝；
- 负向调整不能低于 0；
- 生成审计日志。

依赖：TICKET-007。

---

## TICKET-011 消费退款服务

范围：

- 全额退款
- reference_transaction_id
- 每笔消费最多退一次
- 复用给“撤销上一笔”

验收：

- 退款产生新流水；
- 原消费永久保留；
- 重复退款被拒绝。

依赖：TICKET-007。

# Epic 3：餐次与动态二维码

## TICKET-012 餐次配置

范围：

- 早餐/午餐/晚餐
- 名称/时间/金额/启用
- 时间重叠校验
- 当前餐次查询

验收：

- 时间重叠无法保存；
- 非餐次时间返回无有效餐次；
- 修改金额立即对新交易生效。

依赖：TICKET-004。

---

## TICKET-013 动态 Payment Token

范围：

- CSPRNG Token
- >=128 bit
- token_hash
- 30 秒刷新
- 当前 + 上一个 Token
- 约 60 秒窗口
- 过期处理

验收：

- 二维码不含业务身份信息；
- DB 无原始 Token；
- 过期 Token 无法消费。

依赖：TICKET-006。

---

## TICKET-014 H5 二维码页面

范围：

- 动态码显示
- 30 秒刷新
- 倒计时/刷新状态
- 余额
- 消费记录
- 个人资料
- 登录失效提示

验收：

- 页面持续刷新二维码；
- 余额变更可刷新看到；
- 被新设备顶下线后页面进入登录状态。

依赖：TICKET-006、013。

# Epic 4：消费引擎

## TICKET-015 终端模型与认证

范围：

- terminals
- terminal credential
- heartbeat
- online/offline
- terminal_id

验收：

- 未认证设备不能调用内部消费 API；
- 管理后台可查看心跳。

依赖：TICKET-002、004。

---

## TICKET-016 消费核心事务

范围：

实现：

```text
Token
→ Session
→ User
→ Account
→ Meal
→ Balance
→ Transaction
```

包含 SQLite 原子事务。

验收：

- 扣款和流水原子；
- 余额不足拒绝；
- 冻结拒绝；
- 非餐次拒绝；
- transaction_no 唯一。

依赖：TICKET-007、012、013、015。

---

## TICKET-017 Token 重放幂等

范围：

- 同 Token 只处理一次
- idempotency_key
- 返回首次终态结果
- 并发竞争处理

验收：

并发 10 次提交同一 Token：

```text
消费流水 = 1
余额只减少 1 次
```

依赖：TICKET-016。

---

## TICKET-018 同餐次重复消费确认

范围：

- 查询同用户同餐次已有消费
- pending confirmation
- 30 秒有效
- 用户终端点击继续
- 确认后直接消费

验收：

- 新 Token 二次消费不会直接扣；
- 超时后不能确认；
- 有效确认只产生一笔新增消费。

依赖：TICKET-016、017。

---

## TICKET-019 扫码事件日志

范围：

- 失败扫码事件
- 非资金事件
- token fingerprint
- 禁止记录完整 Token

验收：

各异常可查询：

- INVALID_TOKEN
- TOKEN_EXPIRED
- ACCOUNT_FROZEN
- BALANCE_INSUFFICIENT
- DUPLICATE_SCAN
- DUPLICATE_MEAL
- DEVICE_OFFLINE

依赖：TICKET-016。

# Epic 5：Scan Agent 与终端屏

## TICKET-020 Scan Agent 设备抽象层

目标：先实现可替换扫码设备适配。

范围：

接口抽象：

```text
Scanner
├── SerialScanner
└── HIDScanner
```

优先完成虚拟串口适配。

验收：

- 可配置设备路径；
- 扫码产生统一事件；
- 设备拔出后不会导致进程退出。

依赖：TICKET-001。

---

## TICKET-021 Scan Agent 防抖与消费调用

范围：

- 本地防抖
- 调用内部消费 API
- 网络重试
- 不直接访问 DB

验收：

- 短时间完全相同输入不重复提交；
- 后端不可达时明确报错；
- 恢复后自动继续工作。

依赖：TICKET-015、020。

---

## TICKET-022 终端全屏状态页

范围：

默认状态：

```text
请扫码
```

成功状态：

- 照片
- 姓名
- 金额
- 余额
- 时间

失败状态：

- 明确错误原因

重复餐次：

- 继续消费按钮

设备异常：

- 扫码设备离线

验收：

- 正常消费无需点击；
- 成功信息约 3 秒恢复；
- 重复消费可确认；
- 扫码枪离线醒目提示。

依赖：TICKET-018、021。

---

## TICKET-023 语音与提示音

范围：

- 成功语音
- 失败警告音
- 模块健康状态

验收：

- 成功播报余额；
- 语音失败不会影响已完成交易；
- 管理页可看到语音状态。

依赖：TICKET-022。

# Epic 6：管理后台

## TICKET-024 管理后台人员与账户页

范围：

- 人员列表
- 搜索
- 编辑
- 冻结/解冻
- 余额查看
- 充值
- 调整
- 重置密码

验收：

所有操作均走已有服务，禁止页面直接形成特殊账务旁路。

依赖：TICKET-005、008、010。

---

## TICKET-025 交易流水与退款页

范围：

查询条件：

- 姓名/工号
- 日期
- 餐次
- 类型/状态
- 终端

操作：

- 退款
- 查看关联原交易

验收：

- 已退款状态清晰；
- 不能重复退款；
- 原/反向流水可关联查看。

依赖：TICKET-011、016。

---

## TICKET-026 管理后台配置、终端与日志页

范围：

- 餐次配置
- 终端状态
- Scan Agent 状态
- 扫码事件
- 审计日志
- 系统配置

验收：

- 可识别扫码枪离线；
- 安全参数只读或不开放修改；
- 日志分页和基础筛选可用。

依赖：TICKET-012、015、019、023。

---

## TICKET-027 Dashboard

范围：

- 总人数
- 正常/冻结账户
- 总余额
- 三餐消费人数
- 今日消费
- 今日充值
- 异常扫码
- 终端状态

验收：

统计结果与流水数据一致。

依赖：TICKET-016、026。

# Epic 7：导入导出、日结和运维

## TICKET-028 Excel 人员导入与业务导出

范围：

人员导入：

```text
上传 → 校验 → 预览 → 确认
```

支持：

- 重复工号策略
- 部分成功
- 错误报告

导出：

- 人员
- 余额
- 充值
- 消费
- 退款
- 调整
- 日结

验收：

- 错误行可定位；
- 导出金额和页面一致；
- 防 Excel 公式注入。

依赖：TICKET-005、025。

---

## TICKET-029 每日 00:00 日结

范围：

- opening
- recharge
- reversal
- consume
- refund
- adjustment
- expected closing
- actual closing
- difference
- status

验收：

- 正常账务 `MATCHED`；
- 构造异常后 `MISMATCH`；
- 系统绝不自动修改余额。

依赖：TICKET-008～011、016。

---

## TICKET-030 SQLite 自动备份

范围：

- 每日备份
- 保留 7 天
- 外部路径
- 手动备份 API
- 安全一致性备份

验收：

- WAL 模式下备份可恢复；
- 过期备份自动清理；
- 外部目录不可写时明确报警。

依赖：TICKET-002、004。

---

## TICKET-031 systemd 与生产部署

范围：

```text
canteen-server.service
scan-agent.service
```

包含：

- 开机自启
- Restart=on-failure
- 环境变量
- 日志
- 权限
- 扫码设备访问规则

验收：

- 重启 Linux 后自动恢复；
- kill 进程后 systemd 可拉起；
- 扫码枪拔插后自动恢复。

依赖：TICKET-021、030。

# Epic 8：质量与上线

## TICKET-032 账务自动化测试

必须覆盖：

- 充值
- 冲正
- 消费
- 退款
- 调整
- 负余额保护
- 重复退款保护
- 重复冲正保护

验收：所有资金测试通过。

依赖：Epic 2、TICKET-016。

---

## TICKET-033 并发与幂等测试

测试：

- 同 Token 并发提交
- 同账户多个 Token 并发
- 重复确认
- 退款并发
- 充值/消费竞争

验收：

- 无重复扣款；
- 无负余额；
- 无余额与流水不一致。

依赖：TICKET-017、018。

---

## TICKET-034 现场故障演练

演练：

- Linux 断电
- 服务崩溃
- 扫码枪拔出
- 扫码枪重插
- 磁盘备份失败
- 网络短断
- 语音失败

验收：

- 资金数据不损坏；
- 核心服务自动恢复；
- 异常可观察。

依赖：TICKET-031。

---

## TICKET-035 MVP 端到端验收

按 SPEC 的 25 条 MVP 场景逐项验收。

最终门禁：

```text
不得存在：
- 重复扣款缺陷
- 可直接改余额接口
- 可删除资金流水功能
- 数据库公网暴露
- Token 明文落日志
```

依赖：全部 Ticket。

# 推荐里程碑

## Milestone 1：账务闭环

完成：

```text
001～012
```

可以在无扫码枪情况下完成：

```text
人员 → 充值 → 模拟消费 → 退款 → 调整
```

## Milestone 2：二维码消费闭环

完成：

```text
013～019
```

可以通过 API/模拟器完成：

```text
H5动态码 → 消费 → 幂等 → 重复餐次确认
```

## Milestone 3：真实设备闭环

完成：

```text
020～023
```

真实扫码枪能够完成消费。

## Milestone 4：后台和运营

完成：

```text
024～030
```

达到日常管理可用状态。

## Milestone 5：生产上线

完成：

```text
031～035
```

通过故障演练和最终验收后上线。
