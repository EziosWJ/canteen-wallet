# 二进制部署

## 1. 构建

构建机需要 Task、Go 1.26、Node.js 和 npm。在仓库根目录执行：

```sh
task build
```

生成：

```text
build/canteen-server
build/canteen-scan-agent
build/canteen-scan-sim
```

## 2. 部署服务端

把 `build/canteen-server`、`deploy/canteen.env.example` 和 `deploy/systemd/canteen-server.service` 复制到目标 Linux 服务器，执行：

```sh
sudo useradd --system --home /var/lib/canteen --shell /usr/sbin/nologin canteen 2>/dev/null || true
sudo install -d -o canteen -g canteen -m 700 /var/lib/canteen /mnt/canteen-backup /opt/canteen /etc/canteen
sudo install -o canteen -g canteen -m 755 build/canteen-server /opt/canteen/canteen-server
sudo install -o root -g root -m 600 deploy/canteen.env.example /etc/canteen/canteen.env
sudo install -o root -g root -m 644 deploy/systemd/canteen-server.service /etc/systemd/system/canteen-server.service
```

编辑 `/etc/canteen/canteen.env`，确认：

```env
CANTEEN_PUBLIC_ADDR=127.0.0.1:5001
CANTEEN_INTERNAL_ADDR=127.0.0.1:8081
CANTEEN_DB_PATH=/var/lib/canteen/canteen.db
CANTEEN_TIME_ZONE=Asia/Shanghai
CANTEEN_BACKUP_EXTERNAL_DIR=/mnt/canteen-backup
```

启动服务并创建管理员：

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now canteen-server.service
sudo -u canteen env CANTEEN_DB_PATH=/var/lib/canteen/canteen.db /opt/canteen/canteen-server create-admin admin
```

通过 `http(s)://服务器地址/` 访问系统。正式环境将 HTTPS 反向代理转发到 `127.0.0.1:5001`。

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
sudo -u canteen env CANTEEN_DB_PATH=/var/lib/canteen/canteen.db /opt/canteen/canteen-server restore-backup /mnt/canteen-backup/<备份文件>
```
