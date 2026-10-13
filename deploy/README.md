# Linux 部署与恢复

## Docker Compose

项目提供 Debian slim 运行镜像，前端静态资源在构建时嵌入服务。需要 Linux Docker Engine 和 Docker Compose。Compose 加入已有 external network `1panel-network`，应用容器地址配置为 `172.18.0.10`；请先确认该地址在目标网络未被占用。容器端口分别映射到宿主机 loopback 的 `127.0.0.1:5001`（公开 Web）和 `127.0.0.1:8081`（内部终端 API），不直接向公网发布。1Panel OpenResty 的 HTTPS 站点将公开 upstream 指向 `http://127.0.0.1:5001`。

完整多阶段镜像可在有 Docker 构建器的机器上用根目录 `Dockerfile` 构建。若构建器没有 Docker daemon，可先运行 `task web:embed`，再用 `cd backend && CGO_ENABLED=0 go build -tags=timetzdata -trimpath -ldflags='-s -w' -o canteen-server ./cmd/server` 生成静态二进制，将它与 `deploy/Dockerfile.runtime` 和系统 CA bundle (`/etc/ssl/certs/ca-certificates.crt`) 一起放入镜像上下文，生成 Debian slim 运行镜像；时区数据库嵌入二进制，因此运行镜像步骤不在服务器安装软件包或编译 Go/前端。

SQLite 数据库和应用备份分别持久化到仓库根目录的 `data/` 与 `backups/`，都由 Compose bind mount 到容器。首次启动前创建目录并授予镜像内 `canteen` 用户（UID/GID 10001）写权限：

```sh
mkdir -p data backups
sudo chown -R 10001:10001 data backups
docker compose -f deploy/compose.yaml up --build -d
```

查看日志使用 `docker compose -f deploy/compose.yaml logs -f`；停止服务使用 `docker compose -f deploy/compose.yaml down`。删除 `data/` 会删除正式数据库，升级或重建镜像不需要删除挂载目录。首次运行后可用 `docker compose -f deploy/compose.yaml exec -it canteen-server /usr/local/bin/canteen-server create-admin admin` 创建管理员（将 `admin` 改成实际用户名）。不要把数据库目录放在临时容器文件系统中。

Compose 部署的 SQLite 快照保存在 `backups/`，首次启动时会创建 daily 与 monthly 快照，之后按日保留。恢复时先停止服务，将数据库及可能存在的 WAL sidecar 改名留存；应用的 `restore-backup` 会校验 SQLite 完整性，并拒绝覆盖已存在的目标文件：

```sh
cd /opt/canteen-wallet
sudo docker compose -f deploy/compose.yaml stop canteen-server
stamp=$(date +%Y%m%d%H%M%S)
for suffix in '' -wal -shm; do
  file="data/canteen.db${suffix}"
  if [ -e "$file" ]; then sudo mv "$file" "$file.before-restore-$stamp"; fi
done
IMAGE_ID=$(sudo docker compose -f deploy/compose.yaml images -q canteen-server)
BACKUP_NAME='daily-<选择的备份文件名>.db'
sudo docker run --rm --network none --read-only --tmpfs /tmp:rw,nosuid,nodev --user 10001:10001 \
  -v "$PWD/data:/var/lib/canteen" -v "$PWD/backups:/mnt/canteen-backup:ro" \
  -e CANTEEN_DB_PATH=/var/lib/canteen/canteen.db \
  "$IMAGE_ID" restore-backup "/mnt/canteen-backup/$BACKUP_NAME"
sudo docker compose -f deploy/compose.yaml start canteen-server
```

部署目录内的 daily/monthly 快照仍与服务器处于同一主机；还需另行复制到异机或对象存储，才能满足异地备份要求。

先运行 `task build`，将 `build/canteen-server`、`build/canteen-scan-agent` 安装到 `/opt/canteen/`。创建 `canteen` 系统用户、`/var/lib/canteen` 和仅该用户可写的外部备份挂载 `/mnt/canteen-backup`。将 `deploy/canteen.env.example` 复制为 `/etc/canteen/canteen.env`，按现场地址和设备路径修改。服务端公开入口可通过 HTTP 或 HTTPS 访问，终端 API 使用同一入口与 key 鉴权；8081 保留为兼容的内部入口，8090 为 Agent 本地终端屏入口。

在后端目录配置好 `CANTEEN_DB_PATH` 后，运行 `canteen-server create-admin <username>` 创建至少两名管理员。随后分别执行 `canteen-server enroll-admin-totp <username>`，把输出的 `otpauth://` URI 安全导入各管理员独立的验证器。该命令也用于丢失第二因素后的受审计恢复，执行时会撤销该管理员旧会话。保管 URI，不写入日志或仓库。

在后台创建扫码终端，并配置 `CANTEEN_SERVER_URL` 与 `CANTEEN_TERMINAL_KEY`；服务端与 Agent 可分机运行。最简启动步骤见 [scan-agent.md](scan-agent.md)。CLI `canteen-server provision-terminal <id> <name>` 和凭据文件方式保留为备用。后台下载配置默认使用串口 `/dev/ttyACM0`、`serial` 模式与 9600 波特率，可按实际设备修改路径和波特率；串口为 8N1，扫码结束须发送回车或换行，canteen 用户须有设备读写权限（通常为 `dialout` 组）。键盘事件扫码枪可选择 `/dev/input/eventX` 与 `evdev` 模式；boot keyboard HID 报告可选择 `/dev/hidrawX` 与 `hidraw` 模式。设备也可通过 udev 使用稳定的 `/dev/canteen-scanner` 别名，并显式设置模式。终端屏浏览器打开 `http://127.0.0.1:8090/terminal`；浏览器只连接本地 Scan Agent，设备凭据不进入页面。

如现场需要语音反馈，设置 `CANTEEN_TTS_COMMAND` 为已安装的语音程序绝对路径。复制两个 systemd unit 到 `/etc/systemd/system/` 后执行 `systemctl daemon-reload`，再启用并启动 `canteen-server.service` 与 `canteen-scan-agent.service`。服务日志可用 `journalctl -u canteen-server -u canteen-scan-agent` 查看。备份每小时检查一次；当日无完整备份时自动进行 SQLite 一致性快照。失败会写入 server 日志，需接入现场的日志告警。管理员也可在后台触发备份。每日保留 30 份，外部月度保留 12 份。

恢复演练：先 `systemctl stop canteen-scan-agent canteen-server`，把旧数据库及 `-wal`、`-shm` 文件移到隔离目录，再以 canteen 用户执行 `canteen-server restore-backup <外部备份文件>`。命令会先验证 SQLite 完整性，只向不存在的目标数据库写入。之后启动服务并检查 `/readyz`、人员余额与最近流水。不要在服务运行时替换数据库。

后端不可达时 Scan Agent 立即拒绝扫码，不缓存 Token。工作人员在纸质或离线表单登记唯一凭据、员工、餐次、营业日及金额；恢复后由管理员在后台登记并逐笔补录。余额不足的记录会保留为异常，待充值或线下结清后再处理。
