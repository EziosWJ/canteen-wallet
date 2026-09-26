# Linux 部署与恢复

先运行 `task build`，将 `build/canteen-server`、`build/canteen-scan-agent` 安装到 `/opt/canteen/`。创建 `canteen` 系统用户、`/var/lib/canteen` 和仅该用户可写的外部备份挂载 `/mnt/canteen-backup`。将 `deploy/canteen.env.example` 复制为 `/etc/canteen/canteen.env`，按现场地址和设备路径修改。公开入口只由 HTTPS 反向代理暴露；8081 和 8090 监听留在本机或现场私网，不转发到公网。

在后端目录配置好 `CANTEEN_DB_PATH` 后，运行 `canteen-server create-admin <username>` 创建至少两名管理员。随后分别执行 `canteen-server enroll-admin-totp <username>`，把输出的 `otpauth://` URI 安全导入各管理员独立的验证器。该命令也用于丢失第二因素后的受审计恢复，执行时会撤销该管理员旧会话。保管 URI，不写入日志或仓库。

执行 `canteen-server provision-terminal <id> <name>` 并将一次性输出的凭据放到 `/etc/canteen/terminal.credential`（权限 0600，所有者 canteen）。将扫码枪的 `/dev/input/eventX` 设为稳定的 `/dev/canteen-scanner` udev 路径，设置 `CANTEEN_SCANNER_MODE=evdev`，并给 canteen 用户读取该设备的权限；若设备仅提供 boot keyboard HID 报告，可改用 hidraw 路径和 `hidraw` 模式。终端屏浏览器打开 `http://127.0.0.1:8090/terminal`；浏览器只连接本地 Scan Agent，设备凭据不进入页面。

如现场需要语音反馈，设置 `CANTEEN_TTS_COMMAND` 为已安装的语音程序绝对路径。复制两个 systemd unit 到 `/etc/systemd/system/` 后执行 `systemctl daemon-reload`，再启用并启动 `canteen-server.service` 与 `canteen-scan-agent.service`。服务日志可用 `journalctl -u canteen-server -u canteen-scan-agent` 查看。备份每小时检查一次；当日无完整备份时自动进行 SQLite 一致性快照。失败会写入 server 日志，需接入现场的日志告警。管理员也可在后台触发备份。每日保留 30 份，外部月度保留 12 份。

恢复演练：先 `systemctl stop canteen-scan-agent canteen-server`，把旧数据库及 `-wal`、`-shm` 文件移到隔离目录，再以 canteen 用户执行 `canteen-server restore-backup <外部备份文件>`。命令会先验证 SQLite 完整性，只向不存在的目标数据库写入。之后启动服务并检查 `/readyz`、人员余额与最近流水。不要在服务运行时替换数据库。

后端不可达时 Scan Agent 立即拒绝扫码，不缓存 Token。工作人员在纸质或离线表单登记唯一凭据、员工、餐次、营业日及金额；恢复后由管理员在后台登记并逐笔补录。余额不足的记录会保留为异常，待充值或线下结清后再处理。
