# IMPLEMENTATION-ORDER：推荐实施顺序

- 日期：2026-09-26
- 依据：ADR-001、SPEC-001、TICKETS-001

# 1. 原则

这个项目最容易出现的问题不是页面，而是：

1. 账务模型后期返工；
2. Token 幂等不严导致重复扣款；
3. Scan Agent 与业务服务耦合；
4. 先做大量后台页面，最后才发现核心交易模型不稳定。

因此开发顺序必须以“账务核心 → Token → 消费事务 → 真实扫码枪 → UI 完善”为主。

# 2. Phase 1：工程与账务核心

执行：

```text
TICKET-001
TICKET-002
TICKET-003
TICKET-004
TICKET-005
TICKET-007
TICKET-008
TICKET-009
TICKET-010
TICKET-011
TICKET-012
```

然后立即执行：

```text
TICKET-032 账务自动化测试
```

本阶段不要急着做漂亮 UI。

退出条件：

```text
新增员工
→ 充值
→ 模拟消费
→ 退款
→ 冲正
→ 余额调整
```

账务全部闭环。

# 3. Phase 2：员工认证与动态码

执行：

```text
TICKET-006
TICKET-013
TICKET-014
```

可并行：

- H5 页面开发
- Token 后端开发

但 Token 协议必须以后端为准。

退出条件：

- 单设备会话工作正常；
- 新设备可顶掉旧设备；
- Token 30 秒刷新；
- 旧 Token 约 60 秒失效；
- DB 不保存原始 Token。

# 4. Phase 3：消费引擎

执行：

```text
TICKET-015
TICKET-016
TICKET-017
TICKET-018
TICKET-019
```

完成后立即执行：

```text
TICKET-033 并发与幂等测试
```

这是项目最重要阶段。

在这一阶段没有通过并发幂等测试前，不建议接真实扫码枪。

# 5. Phase 4：扫码设备

先完成：

```text
TICKET-020
```

建议开发阶段先使用：

```text
虚拟串口 / 模拟扫码输入
```

再执行：

```text
TICKET-021
TICKET-022
TICKET-023
```

真实扫码枪采购时优先确认：

- Linux 兼容；
- USB 虚拟串口模式；
- 可配置扫码后缀；
- 可关闭不需要的蜂鸣/自动重复行为。

# 6. Phase 5：管理后台

执行：

```text
TICKET-024
TICKET-025
TICKET-026
TICKET-027
TICKET-028
```

这些任务在消费核心稳定后可以多人并行。

# 7. Phase 6：日结、备份、部署

执行：

```text
TICKET-029
TICKET-030
TICKET-031
```

退出条件：

- 日结可识别差异；
- SQLite 可恢复；
- Linux 重启服务自动恢复。

# 8. Phase 7：上线验证

执行：

```text
TICKET-034
TICKET-035
```

必须做真实故障演练，不接受只做代码级测试。

# 9. Harness / Codex 建议工作方式

每次只给 Agent 一个 Ticket 或一个强相关 Ticket 组。

推荐提示形式：

```text
读取：
- ADR-001-canteen-stored-value-system.md
- SPEC-001-canteen-stored-value-system-mvp.md
- TICKETS-001-canteen-stored-value-system-mvp.md

执行 TICKET-XXX。

要求：
1. 不修改 ADR 已冻结决策；
2. 先检查现有实现；
3. 只实现该 Ticket 的范围；
4. 补齐自动化测试；
5. 运行测试/构建；
6. 输出修改文件、实现内容、测试结果、剩余风险；
7. 如果发现 Spec 冲突，停止扩展实现，明确指出冲突。
```

# 10. 不建议并行的任务

以下任务应串行或至少以前一项稳定为前提：

```text
TICKET-007 → 008/009/010/011
TICKET-013 → 016
TICKET-016 → 017 → 018
TICKET-017/018 → 033
```

# 11. 可以并行的任务

账务核心稳定后，可并行：

```text
H5 UI
管理后台 UI
Scan Agent 设备层
Excel 导入
终端状态页
```

但这些模块都不得自行复制账务逻辑。

所有资金逻辑必须收敛在 Go Server 的账务/消费服务中。
