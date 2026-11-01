# Ly Route Gateway

Ly Route 网关是面向 **x86_64 / ARM64** 的出口网关发行版。数据面基于 VPP 26.06：
除管理口外，物理数据口默认由 VPP 以 **DPDK UIO** 接管，管理口保留在 Linux 内核；
同时提供 AF_PACKET 兼容回退与受控 AF_XDP 能力。

> **发布仓库说明**：本仓库只包含安装/升级产物、打包工程与第三方组件声明。
> 产品源代码不公开，软件使用受 [EULA](EULA.md) 约束。

## 下载

最新正式版：**[v1.1.3](https://github.com/kwrum1/ly-route/releases/tag/v1.1.3)**

| 平台 | 产物 | 用途 |
| --- | --- | --- |
| x86_64 | [`ly-route-gateway-gui-installer.iso`](https://github.com/kwrum1/ly-route/releases/download/v1.1.3/ly-route-gateway-gui-installer.iso) | 裸机安装（BIOS/UEFI，GUI 安装器） |
| x86_64 | [`ly-route-upgrade-gateway-bookworm-amd64.tar.zst`](https://github.com/kwrum1/ly-route/releases/download/v1.1.3/ly-route-upgrade-gateway-bookworm-amd64.tar.zst) | 1.1.x 原地升级 |
| ARM64 | [`ly-route-gateway-arm64-installer.tar.zst`](https://github.com/kwrum1/ly-route/releases/download/v1.1.3/ly-route-gateway-arm64-installer.tar.zst) | Armbian Bookworm 一键安装 |
| ARM64 | [`ly-route-upgrade-gateway-bookworm-arm64.tar.zst`](https://github.com/kwrum1/ly-route/releases/download/v1.1.3/ly-route-upgrade-gateway-bookworm-arm64.tar.zst) | ARM64 原地升级 |

校验文件：[`SHA256SUMS`](https://github.com/kwrum1/ly-route/releases/download/v1.1.3/SHA256SUMS)

## 安装

### x86_64：ISO 安装

1. 下载 ISO，用 `dd` 或 balenaEtcher 写入 U 盘；
2. 目标机选择 BIOS 或 UEFI 启动，进入 GUI 安装器；
3. 选择目标磁盘、管理网口，设置管理地址与管理员密码；
4. 执行安装并重启。

无 DHCP 时安装器回退到 `10.1.18.240/24`。安装完成后数据口按规则库自动接管为
`dpdk_uio`，控制面、VPP 与业务服务自动启动。首次登录强制修改密码。

### ARM64：Armbian Bookworm 一键安装

要求：已安装 **Armbian Bookworm arm64**（保留板卡内核 / DTB / 引导，不改 `/boot`），
root 权限，可访问 Debian 软件源；建议至少两个网口。

```bash
curl -fL -o /tmp/ly-route-arm64.tar.zst \
  https://github.com/kwrum1/ly-route/releases/latest/download/ly-route-gateway-arm64-installer.tar.zst
mkdir -p /tmp/ly-route-installer
tar --use-compress-program=unzstd -xf /tmp/ly-route-arm64.tar.zst -C /tmp/ly-route-installer
cd /tmp/ly-route-installer/ly-route-gateway-arm64-installer
sha256sum -c checksums.sha256
sudo ./install.sh
```

安装脚本幂等，可重复执行；已有 `/etc/ly-route/runtime.env` 与
`/etc/ly-route/appliance.env` 不会被覆盖。默认管理地址 `https://192.168.88.254/`，
初始密码见 `/root/ly-route-initial-admin-password`。

### 升级

Web 控制台：**系统 → 固件升级 → 上传 → 安装**。

API：

```bash
curl -b cookies -X POST https://<gateway>/api/v1/firmware/update/stage \
  -F firmware=@ly-route-upgrade-gateway-bookworm-amd64.tar.zst
curl -b cookies -X POST https://<gateway>/api/v1/firmware/update/install \
  -H 'Content-Type: application/json' -d '{"confirm_install":true}'
```

升级保留现场 `runtime.env` / `appliance.env` 与业务配置；失败时由
`ly-route-recovery.service` 回滚到上一已提交代，严重故障可用 ISO 重装。

升级后检查：

```bash
/usr/lib/ly-route/ly-route-control --version   # 期望 1.1.3
curl -sk https://127.0.0.1/api/v1/health
```

## 功能

| 领域 | 能力 |
| --- | --- |
| 数据面 | DPDK UIO 自动接管、AF_PACKET 兼容回退、受控 AF_XDP |
| 路由 | LAN/WAN、多 WAN、链路聚合、策略路由、ABF、pre-NAT 分流 |
| NAT | NAT44 ED / 全锥、端口映射、hairpin 边界 |
| DNS | SmartDNS + dnsmasq、53 透明劫持、GeoSite、DoH、DNS 数据面自检 |
| 代理 | Xray 出口、服务 TAP、ABF 引流、故障放行 |
| 安全 | 安全 ACL、攻击防护、IP-MAC 绑定、失败关闭 |
| QoS | policer + fq-codel 整形 |
| 运维 | 配置依赖图、事务化 apply、证据回读、supersede 收敛、自愈 |
| 可观测 | 链路双值（能力/实际）、健康探针、在线用户、连接与流量遥测 |

## 系统要求

- **x86_64**：4 GB 以上系统盘、至少 2 个网口（1 管理 + 1 数据）、支持 UIO 的网卡；
- **ARM64**：Armbian Bookworm arm64、root、至少 2 个网口；
- 管理口保留在 Linux 内核，不参与数据面转发。

## 完整性校验

```bash
sha256sum -c SHA256SUMS
```

v1.1.3 的 SHA256：

```text
46313465216290e06afcaedb376e365661f7a72968a6e3e531adaba99062b879  ly-route-upgrade-gateway-bookworm-amd64.tar.zst
da580b164ad8de91e881ac6a34b30e2ae126e3fd0da9fa710a2c06cb85c43fe7  ly-route-gateway-gui-installer.iso
23298844e68a6473df4544cee5ae9e891137db6c17053be4be730890b4abd0d0  ly-route-gateway-arm64-installer.tar.zst
26589adc7f4822e4c43904f71b45eb2ce4172823d8e52983f46793c655a60d53  ly-route-upgrade-gateway-bookworm-arm64.tar.zst
```

## 仓库结构

```text
.
├── README.md                 产品说明与下载
├── EULA.md                   最终用户许可协议
├── NOTICE.md                 版权与商标声明
├── THIRD-PARTY.md            第三方组件清单与合规说明
├── SECURITY.md               安全问题报告方式
├── licenses/                 随附的许可证全文（Apache-2.0 / GPL / MPL / BSD）
├── docs/                     安装、升级与打包说明
├── packaging/                ISO 组装脚本（只做产物组装，不含产品源码）
└── .github/workflows/        GitHub Actions：从二进制输入组装 ISO 并发布
```

## 打包与 CI

本仓库的 CI **不编译产品源码**。它从已发布的二进制输入组装安装 ISO：

1. 目标版本的升级包（发布资产或 `package_url`）；
2. 上一版本的安装 ISO（作为启动骨架与基础 rootfs）。

流程见 [docs/PACKAGING.md](docs/PACKAGING.md) 与
[`packaging/build-gateway-iso-from-base.sh`](packaging/build-gateway-iso-from-base.sh)。

## 许可证与第三方组件

- 产品本身为闭源商业软件，使用条款见 [EULA.md](EULA.md)；
- 第三方组件按各自许可证授权，清单、版本与上游源码地址见 [THIRD-PARTY.md](THIRD-PARTY.md)；
- 本产品包含 GPL / MPL / Apache-2.0 等组件，相关许可证全文见 [`licenses/`](licenses/)。

## 问题反馈

- 功能与安装问题：GitHub Issues；
- 安全漏洞：请勿公开提交，按 [SECURITY.md](SECURITY.md) 私下报告。
