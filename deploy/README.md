# 二进制部署

## 1. 构建

构建机需要 Task、Go 1.26、Node.js 和 npm。在仓库根目录执行：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS=-tags=timetzdata task build
```

生成：

```text
build/canteen-server
build/canteen-scan-agent
build/canteen-scan-sim
```

## 2. 部署服务端

服务端和前端都包含在一个二进制中，服务器无需安装 Go、Node.js 或应用容器。以上构建命令面向 Linux x86_64。

上传到服务器（替换 SSH 地址）：

```sh
scp build/canteen-server deploy/canteen-server.env.example deploy/systemd/canteen-server.service ubuntu@服务器地址:/tmp/
```

首次安装时，在服务器执行：

```sh
id canteen >/dev/null 2>&1 || sudo useradd --system --home /opt/canteen-wallet --shell /usr/sbin/nologin canteen
sudo install -d -o root -g root -m 755 /opt/canteen-wallet
sudo install -d -o canteen -g canteen -m 700 /opt/canteen-wallet/data /opt/canteen-wallet/backups
sudo install -o root -g root -m 755 /tmp/canteen-server /opt/canteen-wallet/canteen-server
sudo install -o root -g root -m 600 /tmp/canteen-server.env.example /opt/canteen-wallet/canteen.env
sudo install -o root -g root -m 644 /tmp/canteen-server.service /etc/systemd/system/canteen-server.service
```

配置文件是 `/opt/canteen-wallet/canteen.env`：

```env
CANTEEN_PUBLIC_ADDR=127.0.0.1:5001
CANTEEN_INTERNAL_ADDR=127.0.0.1:8081
CANTEEN_DB_PATH=/opt/canteen-wallet/data/canteen.db
CANTEEN_TIME_ZONE=Asia/Shanghai
CANTEEN_BACKUP_EXTERNAL_DIR=/opt/canteen-wallet/backups
```

启动服务：

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now canteen-server.service
curl --fail http://127.0.0.1:5001/readyz
```

通过 `http(s)://服务器地址/` 访问系统。正式环境将 HTTPS 反向代理转发到 `127.0.0.1:5001`。新数据库首次访问后台时创建管理员；迁移时直接沿用已有账号和数据。

从 Docker 迁移时，先停止原食堂容器并备份整个 `data` 目录（包含 SQLite WAL），再启动 systemd。现有 `data`、`backups` 可原地保留，但须归 `canteen` 用户所有。取消旧容器自动重启，避免重启服务器后抢占端口；保留旧容器和迁移前备份用于回退。最新版会自动迁移数据库，回退旧版可能需要同时恢复迁移前数据库。

后续更新只需重新构建、上传二进制，然后在服务器执行：

```sh
sudo systemctl stop canteen-server
sudo cp -a /opt/canteen-wallet/data /opt/canteen-wallet/backups/predeploy-$(date +%Y%m%d-%H%M%S)
sudo cp -p /opt/canteen-wallet/canteen-server /opt/canteen-wallet/canteen-server.previous
sudo install -o root -g root -m 755 /tmp/canteen-server /opt/canteen-wallet/canteen-server
sudo systemctl start canteen-server
curl --fail http://127.0.0.1:5001/readyz
```

## 3. 部署扫码 Agent（可选）

先在后台创建终端并取得 key。把 `build/canteen-scan-agent`、`deploy/canteen.env.example` 和 `deploy/systemd/canteen-scan-agent.service` 复制到扫码机，执行：

```sh
sudo useradd --system --home /var/lib/canteen --shell /usr/sbin/nologin canteen 2>/dev/null || true
sudo install -d -o canteen -g canteen -m 700 /opt/canteen /etc/canteen
sudo install -o canteen -g canteen -m 755 build/canteen-scan-agent /opt/canteen/canteen-scan-agent
sudo install -o root -g root -m 600 deploy/canteen.env.example /etc/canteen/canteen.env
sudo install -o root -g root -m 644 deploy/systemd/canteen-scan-agent.service /etc/systemd/system/canteen-scan-agent.service
```

编辑 `/etc/canteen/canteen.env` 中的 Agent 配置：

```env
CANTEEN_SERVER_URL=https://服务器地址
CANTEEN_TERMINAL_KEY=后台创建终端后获得的key
CANTEEN_SCANNER_DEVICE=/dev/ttyACM0
CANTEEN_SCANNER_MODE=serial
CANTEEN_SCANNER_BAUD=9600
CANTEEN_DISPLAY_ADDR=127.0.0.1:8090
```

启动：

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now canteen-scan-agent.service
```

扫码机浏览器打开 `http://127.0.0.1:8090/terminal`。systemd 已为 Agent 配置 `input` 和 `dialout` 附加组。

## 4. 运维

```sh
sudo journalctl -u canteen-server -u canteen-scan-agent -f
sudo systemctl stop canteen-scan-agent canteen-server
```

数据库路径由 `CANTEEN_DB_PATH` 指定，备份目录由 `CANTEEN_BACKUP_EXTERNAL_DIR` 指定。恢复前必须停止服务：

```sh
sudo -u canteen env CANTEEN_DB_PATH=/opt/canteen-wallet/data/canteen.db /opt/canteen-wallet/canteen-server restore-backup /opt/canteen-wallet/backups/<备份文件>
```

恢复前还须移走现有数据库及其 `-wal`、`-shm` 文件；恢复命令不会覆盖已有数据库。
