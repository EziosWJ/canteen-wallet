# SPEC-002 验收测试（issue #21）

本目录是「就餐码恢复、扫码结果同步与同餐次再次消费确认」的可自动化验收套件。

## 运行

```bash
cd backend
go test ./internal/acceptance/           # 全套，约 20 秒
go test ./internal/acceptance/ -v        # 逐用例输出
go test ./internal/acceptance/ -race     # 竞态检查，约 80 秒
```

不需要预热数据、外部服务或环境变量：每个用例各自在 `t.TempDir()` 里迁移一个临时
SQLite 库，并起两个 `httptest` 服务端。

## 边界选择

只通过 HTTP 断言外部可观察行为，不调用内部辅助函数、不检查定时器实现：

| 监听 | 入口 | 用例中的用法 |
| --- | --- | --- |
| 公开 `Public(...)` | `/api/auth/*`、`/api/me/*`、`/api/admin/*` | 员工会话、发码、出示状态查询、确认/取消、管理员充值/退款/餐次配置、扫码事件与资金流水查询 |
| 内部 `Internal(...)` | `/api/v1/terminal/*` | 设备凭据鉴权、扫码、待确认状态、事件流 |

断言对象是 HTTP 状态码与响应、储值余额、资金流水笔数与金额、扫码事件、以及持久化
终态；员工页与终端看到的同一事实必须一致。

## 时间控制

服务直接读取墙上时钟，因此测试通过改写夹具里的时间字段来编排时间窗口，而不是睡眠：

- `ageFirstScanEvents(by)`：把固定 3 秒去重窗口的首次扫码事件整体前移。
- `injectToken(session, flow, issuedAt, validFor)`：按服务的写入方式补发一个已到
  轮换时间点的码（只存哈希，因此仍走正常校验路径）。
- `expirePending(id)` / `setPendingDeadlines(id, mealEnd, expiresAt)`：把待确认请求
  的期限移到过去。
- `endMealNow(code)` / `reconfigureMeal(code, name, price)`：模拟管理员在流程中途
  修改餐次窗口与价格。

仅夹具会写库，断言一律走 HTTP。

## 覆盖对照

用户故事（issue #21）→ 用例：

| 故事 | 用例 |
| --- | --- |
| 1 首次进入立即发码 | `TestFirstVisitIssuesScannableCode` |
| 2、3、4 返回/重载/双标签恢复同一出示 | `TestReturningPageRestoresActivePresentation` |
| 5 按服务端刷新时间轮换 | `TestFirstVisitIssuesScannableCode`、`TestCodeRotationKeepsPreviousCodeUsable` |
| 6 倒计时按服务端时间校正 | `TestFirstVisitIssuesScannableCode`（`server_time` 与客户端时钟偏差 <5s） |
| 7、8 断网缓存码与过期隐藏 | `TestExpiredAndMissingPresentationStates`、`TestLogoutAndRevocationClearPresentationAccess`（服务端侧） |
| 9 恢复联网获知结果 | `TestEmployeeLearnsScanResultAfterReconnect` |
| 10 前台轮询/后台暂停 | `TestPresentationPollingIsIdempotent`（轮询无副作用且立即反映最新状态） |
| 11 旧码被扫也收到结果 | `TestOldCodeScanStillReportsResult` |
| 12、13、14 成功详情与返回可见 | `TestEmployeeLearnsScanResultAfterReconnect`、`TestSuccessDetailSurvivesReload` |
| 15、16 停止等待与明确失败后重试 | `TestEmployeeStopsWaitingAndStartsNewFlow`、`TestNoActiveMealFailsWithoutCharging`、`TestInsufficientFundsIsFinalForTheCode` |
| 17 待确认展示餐次/金额/期限 | `TestPendingConfirmationShowsMealAmountAndDeadline` |
| 18 仅本人有效原会话可确认 | `TestOnlyOwningSessionCanConfirm`、`TestConfirmationRejectsInvalidatedSession` |
| 19 可取消 | `TestEmployeeCanCancelPendingConfirmation` |
| 20 超时与餐次结束失效 | `TestPendingExpiresAtItsDeadline`、`TestMealEndCutsTheDeadlineShort` |
| 21 期限独立于码轮换 | `TestDeadlineIsIndependentOfCodeRotation`、`TestMealConfigChangeDoesNotInvalidatePending` |
| 22 按扫码快照扣款 | `TestConfirmationUsesScannedAmountSnapshot` |
| 23 重复确认幂等 | `TestRepeatedConfirmAndCancelAreIdempotent` |
| 24 已全额退款不再触发确认 | `TestRefundedConsumptionDoesNotTriggerConfirmation`、`TestSecondConsumptionAfterWindowNeedsNewCode` |
| 25 终端得到成功/失败/待确认 | `TestTerminalSeesEveryScanOutcome` |
| 26 3 秒内重复返回首次结果 | `TestRepeatInsideWindowMergesIntoFirstResult`、`TestWindowIsNotExtendedByRepeats` |
| 27 同码成功后重放 | `TestSameCodeReplayAlwaysReturnsFirstResult` |
| 28 跨终端同一等待状态 | `TestCrossTerminalShowsSameWaitingState` |
| 29 终端无确认入口 | `TestTerminalHasNoConfirmAction` |
| 30 断线后按编号恢复 | `TestTerminalResumesByPendingID`、`TestTerminalResumesAllOutstandingConfirmations` |
| 31 服务端不可达明确拒绝、不离线补扣 | `TestUnreachableServerRejectsWithoutOfflineDeduction` |
| 32 重复触发留痕不产生资金流水 | `TestDuplicateTriggerLeavesNoExtraFundsFlow` |
| 33 历史消费保留当时快照 | `TestConsumptionSnapshotSurvivesConfigChange` |
| 34 并发只有一个终态 | `TestConcurrentScansHaveOneFinalState`、`TestConcurrentDecisionHasOneFinalState` |
| 余额不足 | `TestInsufficientFundsIsFinalForTheCode`、`TestConfirmationChecksFundsAtConfirmationTime` |

## 未被本套件覆盖

只通过 HTTP，无法替代浏览器的作用：

- 故事的**页面侧**：本地缓存的写入/清除、标签页 `storage` 事件同步、`visibilitychange`
  暂停与恢复、断网时隐藏过期码的渲染、成功页自动跳转。这些需要前端测试（当前仓库
  没有 vitest/playwright 依赖），本套件只固定了它们依赖的服务端契约。
- `paymentCacheKey`/`readPaymentCache` 等前端缓存工具没有测试。
- 充值冲正、余额退还、日结与管理员复核流程超出 #21 范围，未覆盖。
- 并发用例的深度有限：服务以单写连接运行（`store.Open` 的 `SetMaxOpenConns(1)`），
  `database/sql` 会把请求串行化，因此这些用例验证的是 HTTP 层交错，而不是真正并行的
  事务竞争。

## 实现行为观察（非缺陷）

写套件时确认的几点行为，供后续维护参考：

1. **重复扫码在响应与事件上有两种标记，选择取决于命中哪条路径。** 命中固定 3 秒
   去重窗口的扫码返回 `duplicate_trigger=true`，事件记为 `DUPLICATE_TRIGGER`；由单码
   或出示流程重放处理的扫码返回 `replayed=true`，事件记为 `TOKEN_REPLAY`。两者都只
   返回首次结果、都留扫码事件、都不产生第二笔流水。实测组合（见
   `TestSameCodeReplayAlwaysReturnsFirstResult`、`TestRepeatInsideWindowMergesIntoFirstResult`）：

   | 场景 | `replayed` | `duplicate_trigger` | 事件 |
   | --- | --- | --- | --- |
   | 首次扫码 | false | false | `CONSUMED` 等首次结果 |
   | 同一码，窗口内 | true | true | `DUPLICATE_TRIGGER` |
   | 同一码，窗口外 | true | false | `TOKEN_REPLAY` |
   | 同流程的兄弟码，流程已成功 | true | false | `TOKEN_REPLAY` |
   | 另一个流程的首个码，窗口内 | true | true | `DUPLICATE_TRIGGER` |

   若要按事件统计「设备重复触发」，应把 `DUPLICATE_TRIGGER` 与 `TOKEN_REPLAY` 一并
   纳入；只数 `DUPLICATE_TRIGGER` 会漏掉同码重放。
2. **被去重合并的码会继承首次结果。** 落在窗口内的另一个码被记为重复后，该码本身的
   结果即被固定为首次结果；窗口过后再用它扫码仍返回该结果。因此员工要重新就餐必须
   再出示一个新码，而不是重试同一个码。
3. **待确认期间无法开启新的出示。** 存在未决待确认请求时，`/api/me/payment-token`
   返回 409 `INVALID_STATE`；一个员工在同一业务日期、餐次下最多一个未决请求。
4. **餐次结束的期限在扫码时折算。** `expires_at` 存的是「扫码 + 60s」与原餐次结束时刻
   的较早者，`meal_end_at` 单独保存，过期时用它区分 `MEAL_ENDED` 与 `PENDING_EXPIRED`。
   管理员之后修改餐次窗口不会改动已存快照，因此不影响在途请求（见用例 21）。
5. **待确认的所有失败终态都把出示流程置为 `FAILED`**（含员工取消），出示流程状态
   取值只有 `ACTIVE`/`SUCCESS`/`FAILED`，具体原因在 `result_code` 上。

## 已迁移的旧契约

#21 要求覆盖旧基线的契约迁移，套件据此固定：

- 员工侧存在消费状态查询入口（`GET /api/me/payment-presentation`），且不以原始码作为
  URL 参数；待确认由员工鉴权接口确认/取消。
- 终端确认扣款入口退役：`POST /api/v1/terminal/pending/{id}/{confirm,cancel}` 返回
  404/405，终端只能查询等待与最终结果（`TestTerminalHasNoConfirmAction`）。
