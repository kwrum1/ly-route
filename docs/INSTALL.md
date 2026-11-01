# 安装

## x86_64：ISO 安装

### 准备

- 4 GB 以上系统盘（建议 SSD）；
- 至少 2 个网口：1 个管理口 + 1 个数据口；
- 支持 UIO 的网卡（Intel igb/igc、部分 Broadcom/Mellanox 等，以实际探测为准）；
- U 盘（≥ 2 GB）。

### 写入与启动

```bash
# Linux / macOS，确认 /dev/sdX 是 U 盘后再执行
sudo dd if=ly-route-gateway-gui-installer.iso of=/dev/sdX bs=4M status=progress conv=fsync
```

也可以使用 balenaEtcher、Rufus（DD 模式）等工具。目标机选择 BIOS 或 UEFI 启动。

### 安装向导

1. 选择目标磁盘（会清空该磁盘）；
2. 选择管理网口；
3. 设置管理地址、网关、DNS 与管理员密码；
4. 确认后执行安装，等待写入与校验完成；
5. 重启，拔出 U 盘。

无 DHCP 时安装器回退到 `10.1.18.240/24`。安装完成后：

- 管理口保留在 Linux 内核；
- 数据口按规则库自动接管为 `dpdk_uio`；
- `ly-route-control-api`、VPP 与业务服务自动启动。

### 首次登录

浏览器访问 `https://<管理地址>/`，使用安装时设置的管理员账号登录；首次登录强制
修改密码。若使用 ARM64 安装包，初始密码在 `/root/ly-route-initial-admin-password`。

### 安装后自检

```bash
/usr/lib/ly-route/ly-route-control --version
curl -sk https://127.0.0.1/api/v1/health
```

数据口应显示 `takeover_mode=dpdk_uio`，管理口仍在 Linux。

## ARM64：Armbian Bookworm 一键安装

### 要求

- 已安装 **Armbian Bookworm arm64**；
- 保留板卡内核、DTB 与引导，安装器不修改 `/boot`；
- root 权限；
- 可访问 Debian 软件源（可用 `LY_ROUTE_MIRROR` 指定镜像）；
- 建议至少 2 个网口。

### 安装

```bash
curl -fL -o /tmp/ly-route-arm64.tar.zst \
  https://github.com/kwrum1/ly-route/releases/latest/download/ly-route-gateway-arm64-installer.tar.zst
mkdir -p /tmp/ly-route-installer
tar --use-compress-program=unzstd -xf /tmp/ly-route-arm64.tar.zst -C /tmp/ly-route-installer
cd /tmp/ly-route-installer/ly-route-gateway-arm64-installer
sha256sum -c checksums.sha256
sudo ./install.sh
```

安装脚本幂等，可重复执行。已有 `/etc/ly-route/runtime.env` 与
`/etc/ly-route/appliance.env` 不会被覆盖。

安装完成后：

- Web 控制台：`https://192.168.88.254/`（首次登录强制改密）；
- 初始密码：`/root/ly-route-initial-admin-password`；
- 健康检查：`curl -k https://127.0.0.1/api/v1/health`。

### 端口说明

控制台与 API 由 `ly-route-edge`（`ly-route-pingora.service`）监听 80/443。
安装包不安装、不启用、不改写、不重启 nginx；目标机已有 nginx 保持原样。

```bash
ss -lntp | grep -E ':(80|443)\b'   # 应为 ly-route-edge
systemctl status ly-route-pingora
```

### 可选组件

包内附带 `ly-route-smartdns`、`ly-route-xray` 两个 deb，安装后默认不启用，由控制面
按 `LY_ROUTE_OPTIONAL_UNITS` 按需启停。需要开机自启时手动启用，或安装时设置
`LY_ROUTE_ENABLE_OPTIONAL_SERVICES=1`。

## 故障排查

| 现象 | 处理 |
| --- | --- |
| 安装器找不到磁盘 | 检查 BIOS/UEFI 模式与 RAID 设置，改用 AHCI/JBOD |
| 数据口未接管 | 查看 `ly-route-control` 日志与 `/etc/ly-route/inventory.json` |
| Web 无法访问 | 确认管理口地址、`ly-route-pingora` 与防火墙规则 |
| 升级后服务异常 | 等待 `ly-route-recovery.service` 回滚，或按升级文档回滚 |

更多问题请提交 GitHub Issue，并附上版本、架构、网卡型号与相关日志。
