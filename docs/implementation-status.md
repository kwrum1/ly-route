# 实现与验收状态 / Implementation And Validation Status

更新时间 / Updated: 2026-08-24

## 中文

当前网关 UI、API、持久化、服务编排、VPP 操作生成和发行脚本已经集成。容器验收曾
覆盖 PPPoE、LAN/DHCP、IPv4 路由/NAT、端口映射、VLESS/Reality、SmartDNS、
DNS 透明劫持、策略路由、WAN 群组、QoS、安全和配置事务。历史百分比不再作为当前
结论，只有当前版本可重复证据有效。

最新 x86 ISO 在 ESXi 完成纯净安装：光盘安装、硬盘启动、管理口
`192.168.88.254/24`、HTTPS/API、VPP、SmartDNS、Kea、Nginx 和控制服务正常；
systemd 失败单元为 0。三张 VMXNET3 数据口以验收专用 TAP bridge 进入 VPP，状态
为 `up` 并观察到报文。PPPoE、DNS 拦截、Smart QoS 和安全插件均已加载。

GitHub Actions 已能并行生成 x86 ISO/x86 升级包和 ARM64 Armbian 一键安装包/
ARM64 升级包。ARM rootfs 仅是组装一键安装包的临时输入，不上传、不按设备型号生成。

尚未关闭的生产风险：真实物理 PCI 网卡的 VPP 原生/DPDK 资格验证；真实公网 IPv6
PD 与 RA；长时间 Smart QoS 稳定性和多速率公平性；管理口虚拟化与失联回滚；
VPP 26.x API/CLI/插件迁移；升级包在真实旧版本上的回滚验证。

## English

The Gateway UI, API, persistence, service orchestration, VPP operation rendering
and release builders are integrated. Container evidence covers PPPoE, LAN/DHCP,
IPv4 routing/NAT, port mapping, VLESS/Reality, SmartDNS, transparent DNS,
policy routing, WAN groups, QoS, security and transactional configuration.

The latest x86 ISO passed a clean ESXi installation: disk boot, management at
`192.168.88.254/24`, HTTPS/API and required services. VPP loaded three VMXNET3
ports through the acceptance-only TAP bridge path and loaded the PPPoE, DNS,
Smart QoS and security plugins. No systemd units were failed.

Remaining production risks are physical PCI qualification, real IPv6 PD/RA,
long-duration QoS, virtualized management with rollback, VPP 26.x migration and
upgrade rollback from a real previous release.
