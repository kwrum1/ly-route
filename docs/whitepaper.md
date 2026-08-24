# Ly Route 出口网关白皮书 / Gateway Whitepaper

## 中文

### 产品定位

Ly Route 是以 VPP 为数据面的独立出口网关，面向 x86-64 专用设备和运行 Armbian
Bookworm 的 ARM64 设备。它提供家用及中小型网络需要的路由、拨号、多出口、DNS、
代理、QoS 和安全能力。本仓库不包含流量编排器，编排器使用独立仓库、版本和验收。

### 核心能力

- 物理口、链路聚合、LAN/WAN，支持静态、DHCP 和原生 VPP PPPoE。
- 主备、权重负载、最小连接数和最大下行空闲带宽 WAN 群组。
- 策略路由、Endpoint-dependent NAT、全锥 NAT 和端口映射。
- TCP/UDP 53 透明劫持、域名策略、指定 DNS 上游及指定出口。
- 代理 WAN、VLESS/Reality、订阅节点选择与健康切换。
- IP/域名组、用户限速、内置 Smart QoS 和双向安全策略。
- 在线用户、五元组连接、Top 域名及 WAN 流量遥测。
- 用户管理、配置初始化、升级、失败回滚和运行态恢复。

### 数据面原则

每台设备只使用一个共同合格的数据路径等级。优先验证 VPP 原生高性能路径，DPDK/
VFIO 作为受控回退；均不可用时锁定转发并明确保留管理面，不把 Linux 转发或
AF_PACKET 静默作为生产降级。ESXi 的 TAP/bridge 路径仅用于功能验收，不代表生产
性能或正式数据面支持。

DNS 负责域名分类和解析出口，VPP 负责最终流量分流。代理进程不接管全局策略；只有
VPP 选中的流量进入代理 WAN。配置应用采用事务模型，删除 WAN 等对象前必须处理其
DNS、路由、NAT、群组和代理依赖。

### 明确边界

不包含流量编排器、DPI/OAF、应用识别、用户行为审计、云管理、VRRP/HA、代理群组
或面向用户的底层 Linux 防火墙规则编辑。未经物理设备验证的吞吐量不作为发布承诺。

### 交付形态

- x86-64：可交互安装 ISO 和升级包。
- ARM64：保留板卡内核、DTB、引导程序和 `/boot` 的 Armbian 一键安装包及升级包。
- ARM rootfs 仅为 CI 组装中间产物，不单独发布，也不宣称支持所有 ARM 板卡。

## English

### Product Positioning

Ly Route is a standalone VPP-based egress gateway for x86-64 appliances and
ARM64 devices running Armbian Bookworm. It provides routing, PPPoE, multi-WAN,
DNS, proxy, QoS and security functions. The traffic orchestrator is maintained
in a separate repository with independent releases and validation.

### Core Capabilities

The Gateway supports physical and bonded interfaces, static/DHCP/PPPoE WANs,
multi-WAN policies, policy routing, endpoint-dependent and full-cone NAT, port
mapping, transparent DNS, proxy WANs, object groups, rate limits, Smart QoS,
security policy, telemetry, users, initialization, upgrade and rollback.

### Data Plane Principles

One qualified data-path tier is selected per device. VPP-native integration is
preferred and DPDK/VFIO is the controlled fallback. Forwarding remains locked
when neither qualifies; Linux forwarding and AF_PACKET are not silent
production fallbacks. DNS classifies names, VPP makes the forwarding decision,
and proxy processes receive only traffic selected by VPP.

### Scope And Delivery

The repository excludes orchestration, DPI/OAF, application identification,
behavior auditing, cloud management, VRRP/HA and user-facing low-level Linux
firewall editing. Releases contain an x86-64 installer ISO and upgrade package,
plus an ARM64 Armbian one-click installer and upgrade package. ARM rootfs output
is an internal build input and is not a release artifact.
