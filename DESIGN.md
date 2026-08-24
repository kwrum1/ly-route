# 出口网关设计基线 / Gateway Design Baseline

## 中文

Ly Route 只包含路由式出口网关。产品定位与边界以[白皮书](docs/whitepaper.md)
为准，运行时和打包以[架构](docs/architecture.md)为准。

管理面在数据面锁定时仍必须可达。所有数据口使用设备级统一合格路径：VPP 原生
高性能集成优先，DPDK 为受控回退；Linux 转发和 AF_PACKET 不是生产路径。配置事务
遵循校验、持久化、应用、运行态回读和回滚。UI 只显示路由器用户语义。

## English

Ly Route contains one routed egress Gateway. Product scope follows the
[whitepaper](docs/whitepaper.md), while runtime and packaging follow the
[architecture](docs/architecture.md).

Management remains reachable while forwarding is locked. Data ports use one
device-wide qualified path: VPP-native high performance first and controlled
DPDK fallback second. Linux forwarding and AF_PACKET are not production paths.
Transactions validate, persist, apply, read observed state and roll back.
