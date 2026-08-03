# Ly Route 测试固件安装

语言：简体中文

测试交付的 x86 固件是 4 GiB `amd64` GPT 磁盘镜像，包含 rootfs、Linux kernel、initramfs、GRUB BIOS 启动文件和可移动 UEFI 启动文件。它应直接写入 VM 磁盘、U 盘或设备存储。

## 产物

使用 `dist/disk-image-current/` 中的文件：

```text
ly-route-bookworm-amd64-4g.img
ly-route-bookworm-amd64-4g.img.sha256
ly-route-bookworm-amd64-4g.img.zst
ly-route-bookworm-amd64-4g.img.zst.sha256
```

测试前校验：

```sh
sha256sum -c dist/disk-image-current/ly-route-bookworm-amd64-4g.img.zst.sha256
zstd -t dist/disk-image-current/ly-route-bookworm-amd64-4g.img.zst
```

## 写盘

写入压缩镜像：

```sh
unzstd -c dist/disk-image-current/ly-route-bookworm-amd64-4g.img.zst | sudo dd of=/dev/sdX bs=16M status=progress conv=fsync
```

或写入未压缩镜像：

```sh
sudo dd if=dist/disk-image-current/ly-route-bookworm-amd64-4g.img of=/dev/sdX bs=16M status=progress conv=fsync
```

`/dev/sdX` 必须是整块目标磁盘，容量至少 4 GiB。目标磁盘原有数据会被销毁。

## 出厂网络默认值

- 未预配置 WAN。
- 第一个非 loopback 以太网接口作为 LAN 管理接口。
- LAN 地址：`192.168.88.1/24`。
- DHCP 地址池：`192.168.88.100 - 192.168.88.199`。
- 管理 UI：`https://192.168.88.1/`。
- 默认登录：`admin` / `admin12345`。

首次登录必须修改密码。默认密码未修改前写操作保持阻断。

## 首次启动冒烟

在 LAN 客户端执行：

```sh
ip addr show
ping -c 3 192.168.88.1
curl -k -I https://192.168.88.1/
```

在设备控制台执行：

```sh
systemctl is-active nginx.service ly-route-control-api.service kea-dhcp4-server.service
ip -4 addr show
journalctl -u ly-route-firstboot.service -u ly-route-control-api.service -u kea-dhcp4-server.service --no-pager
```

管理访问成功后，继续执行 `runtime-hardware-validation.md` 中的 VPP、PPPoE、SmartDNS、xray、nftables/TProxy 和实时遥测验证。
