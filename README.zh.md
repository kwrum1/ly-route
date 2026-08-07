# LY-Route

LY-Route 是一个双产品网络设备代码库：

- **出口网关 Gateway**：提供 LAN/WAN、多 WAN、路由/NAT、DNS/DHCP、代理、流量策略、安全、遥测和维护。
- **流量编排器 Orchestrator**：提供双臂透明流量编排、物理端口配对、有序策略和对称服务链。

产品类型在构建时确定，分别拥有 profile、服务、资源、配置、UI 和产物，不能在运行时互转。

设备会自动探测已分配的数据网卡，优先选择实测通过且排名最高的 VPP 原生高性能路径；没有原生候选通过时，才使用 VPP DPDK。两个等级都不满足性能和安全门禁时，管理面继续可用，数据面保持锁定。

文档：[English](docs/README.md) | [简体中文](docs/zh/README.md)

接盘请先阅读[实现状态盘点](docs/zh/implementation-status.md)、[产品功能边界](docs/zh/product-functional-boundary.md)和[后续工作计划](docs/zh/work-plan.md)。构建与镜像说明见 [RootFS 镜像](docs/zh/rootfs-image.md)。
