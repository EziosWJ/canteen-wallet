# 食堂储值卡消费系统文档包

> 日期：2026-09-26  
> 状态：MVP 决策已冻结，可进入开发

本目录包含食堂储值卡消费系统从 ADR 到 Spec，再到 Tickets 的完整开发基线。

## 文档索引

1. `ADR-001-canteen-stored-value-system.md`
   - 架构决策记录
   - 说明为什么采用 Go + React + SQLite WAL + Linux Scan Agent
   - 固化动态二维码、账务、部署、备份和终端方案

2. `SPEC-001-canteen-stored-value-system-mvp.md`
   - MVP 产品与技术规格
   - 包含角色、功能、状态机、数据模型、接口、异常、账务、二维码协议、非功能要求和验收标准

3. `TICKETS-001-canteen-stored-value-system-mvp.md`
   - 可直接进入开发的任务拆分
   - 按依赖关系拆为 9 个 Epic、35 个 Ticket
   - 每个 Ticket 都包含目标、范围、验收标准和依赖

4. `IMPLEMENTATION-ORDER.md`
   - 推荐实施顺序
   - 说明哪些任务可以并行、哪些必须串行
   - 给 Harness/Codex 使用时建议按阶段执行

## MVP 核心链路

```text
管理员建档
→ 充值
→ 员工登录 H5
→ 服务端生成动态二维码
→ Linux Scan Agent 读取扫码枪
→ 后端校验 Token / 账户 / 餐次 / 重复消费
→ SQLite 原子扣款
→ 写不可删除流水
→ 状态屏 + 语音反馈
→ H5 查看余额和消费记录
→ 管理员退款/冲正
→ 每日 00:00 日结校验
```

## 技术基线

```text
Go
React
SQLite WAL
Linux
systemd
USB 扫码枪
HTTPS
每日备份
```

## 明确不在 MVP

- 微信支付 / 支付宝支付
- 短信验证码
- 复杂 RBAC
- 多食堂 / 多租户
- NFC / 实体 IC 卡
- 防截图 / 近场认证
- 补贴账户 + 个人账户双钱包
- 部分退款
- 小票打印
- 第三方系统接口
