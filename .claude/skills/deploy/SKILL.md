---
name: deploy
description: 将 canteen-wallet 当前工作区构建并部署到服务器或扫码机。用户说“部署”“更新部署”“上线”“发布到服务器”时使用。
argument-hint: "[主机地址] [server|agent]"
---

# 部署

把当前工作区构建并部署到目标主机。默认目标 `ubuntu@82.157.189.251`，默认部署服务端。默认不跑测试，直接构建部署。

## 先确认

只在无法从用户消息或参数判断时问，一次问清：

- 目标主机：没给地址就用默认 `82.157.189.251`；给了别的地址就用用户的。
- 部署内容：`server`（默认，服务端+前端）或 `agent`（扫码机 Agent）。
- 工作区有未提交改动时，按当前工作区构建部署即可，不需要额外询问。

## 服务端部署

1. 构建：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS=-tags=timetzdata task build
```

完成标准：`build/canteen-server` 时间戳更新，`file` 显示 ELF x86-64。

2. 上传并校验：

```sh
scp build/canteen-server ubuntu@HOST:/tmp/canteen-server.new
ssh ubuntu@HOST 'sha256sum /tmp/canteen-server.new'
```

本机 `sha256sum build/canteen-server` 与其一致后继续。

3. 停服务、备份数据、换二进制、启动，失败自动回滚：

```sh
ssh ubuntu@HOST 'bash -s' <<'EOS'
set -euo pipefail
BIN=/opt/canteen-wallet/canteen-server
PREV=/opt/canteen-wallet/canteen-server.previous
TS=$(date +%Y%m%d-%H%M%S)
BACKUP=/opt/canteen-wallet/backups/predeploy-$TS
sudo -n systemctl stop canteen-server
sudo -n cp -a /opt/canteen-wallet/data "$BACKUP"
sudo -n cp -p "$BIN" "$PREV"
sudo -n install -o root -g root -m 755 /tmp/canteen-server.new "$BIN"
sudo -n systemctl start canteen-server
for i in $(seq 1 30); do
  curl -sf -m 2 http://127.0.0.1:5001/readyz >/dev/null && { echo "DEPLOY OK backup=$BACKUP"; exit 0; }
  sleep 1
done
echo "健康检查失败，回滚"
sudo -n systemctl stop canteen-server || true
sudo -n install -o root -g root -m 755 "$PREV" "$BIN"
sudo -n systemctl start canteen-server
exit 1
EOS
```

4. 验证：

```sh
ssh ubuntu@HOST 'systemctl is-active canteen-server; curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:5001/; curl -s http://127.0.0.1:5001/ | grep -o "assets/index-[^\"]*\.js"'
```

完成标准：`active`、HTTP 200、页面引用本次构建的 JS 文件名。报告备份路径，以及回滚方式（把 `canteen-server.previous` 装回后重启）。

## 扫码 Agent 部署

`agent` 时构建 `build/canteen-scan-agent` 并部署到扫码机，安装与配置步骤见 `deploy/README.md` 第 3 节。扫码机地址不是默认服务器时先问清楚。

## 拿不准就问

- 用户只说“部署”，且原话看不出是服务端还是扫码机。
- 目标主机上运行的部署方式与 `deploy/systemd/canteen-server.service` 不一致。
- 远程 `sudo -n` 不可用：告知用户需要免密 sudo，或让用户自己执行。
