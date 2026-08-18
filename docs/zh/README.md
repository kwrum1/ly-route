# LY-Route 文档

## 权威文档

- [仓库日常工作入口](../../AGENTS.md)：每次任务只先读取这一页，其他文档按任务类型加载
- [开发、热修复与验收流程](development-workflow.md)：当前唯一有效的最小开发闭环和门禁分层
- [网关与流量编排器产品实现现状报告](product-manager-status-report.md)：面向产品经理的已实现、部分实现、未实现和待确认边界盘点
- [实现状态盘点](implementation-status.md)：当前代码、功能和证据基线
- [后续工作计划](work-plan.md)：功能 backlog 和产品范围记录
- [双产品架构](architecture.md)：Gateway/Orchestrator 架构与数据面路线
- [产品功能边界](product-functional-boundary.md)：必须包含和明确排除的能力
- [产品验收契约](product-functional-qa.md)：功能批次的四点证据标准
- [UI 设计基线](ui-design.md)：参考 iKuai 4.0、项目自有的双产品 UI 体系
- [配置表达模型](configuration-model.md)：any、主机、CIDR、范围与对象引用的统一语义
- [编译机工具链与换行边界](compiler-build-environment.md)：固定 Go 环境以及 Windows/Linux 同步规则

## 交付与运维

- [双产品全功能验收设计](container-network-validation.md)
- [RootFS 镜像](rootfs-image.md)
- [运行时与硬件验证](runtime-hardware-validation.md)
- [测试固件安装](test-firmware-install.md)
- [ISO 打包与安装验收](iso-packaging-and-acceptance.md)：仅用于发布构建和安装冒烟
- [研究门禁](research-gates.md)
- [白皮书](whitepaper.md)

历史调查和已被取代的修复清单保留在[归档](../archive/README.md)，不再代表当前计划或日常门禁。
