# canteen-wallet

食堂储值卡消费系统（Canteen Wallet）。

**当前状态：设计阶段。** 仓库内目前只有需求、规格与接口素材等文档，尚无任何可构建代码——两个子目录下均无 `go.mod` / `package.json`，因此没有可用的安装、构建、测试命令。

## 目录结构

```
canteen-wallet/
├── README.md    # 本文件
├── .gitignore
│
├── canteen-stored-value-system/            # 后端：储值卡消费系统
│   ├── README.md                           # 模块说明
│   ├── ADR-001-canteen-stored-value-system.md          # 架构决策记录
│   ├── SPEC-001-canteen-stored-value-system-mvp.md     # MVP 规格说明
│   ├── TICKETS-001-canteen-stored-value-system-mvp.md  # 任务拆分
│   └── IMPLEMENTATION-ORDER.md             # 实现顺序
│
└── frontpage/                              # 前端
    ├── # 食堂储值卡消费系统.md              # 前端需求文档
    └── canteen-ui-assets/                  # UI 素材
        ├── README.txt
        ├── app-logo.png
        ├── default-avatar.png
        ├── footer-waves.png
        ├── home-header-bg.png
        ├── login-cafeteria-bg.png
        ├── phone-qr-illustration.png
        ├── scan-frame.png
        └── success-illustration.png
```

## 从哪里开始读

| 想了解 | 去看 |
| --- | --- |
| 系统要做成什么样 | `canteen-stored-value-system/SPEC-001-canteen-stored-value-system-mvp.md` |
| 为什么这么设计 | `canteen-stored-value-system/ADR-001-canteen-stored-value-system.md` |
| 拆成了哪些任务、按什么顺序做 | `canteen-stored-value-system/TICKETS-001-...md`、`IMPLEMENTATION-ORDER.md` |
| 后端模块入口 | `canteen-stored-value-system/README.md` |
| 前端页面与视觉 | `frontpage/# 食堂储值卡消费系统.md`、`frontpage/canteen-ui-assets/` |

## 开发环境

- Go 1.26.5（后端，待实现）
- Node.js 24.16.0 / npm 11.13.0（前端，待实现）

> 构建与运行命令在前端工程和后端模块初始化（`go mod init`、前端脚手架）后补充。

## 贡献

1. 从 `main` 切出特性分支。
2. 提交改动并说明影响范围。
3. 涉及设计变更时，同步更新对应的 SPEC / ADR 文档。
