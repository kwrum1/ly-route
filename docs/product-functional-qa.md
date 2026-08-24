# 验收契约 / Verification Contract

## 中文

功能完成必须同时满足：真实 UI 或公开 API 提交、持久化回读、运行服务接收、VPP 或
对应服务运行态可见、独立客户端观察到结果。仅有页面、schema、mock、fixture 或
单元测试不算功能验收。

日常修复使用最小门禁：相关编译/测试、热替换、真实复现和一次定向回归。发行门禁
包括源码验证、产物构建、SHA-256、x86 ISO 安装、ARM 一键安装包结构和升级包结构。
性能只在物理网卡与目标硬件阶段验收，不用 VMware/TAP 数字代表生产性能。

已验收项目不得因测试夹具重启或拓扑变化被直接判为产品回归。首先分层判断会话、
路由、NAT、DNS、代理、QoS、夹具和管理面。修复后只重跑受影响批次及最终发行门禁。

## English

A feature requires configuration through the real UI or public API, persistence
readback, runtime service receipt, observed VPP/service state and an independent
client outcome. A page, schema, mock, fixture or unit test alone is insufficient.

Daily repair uses focused compile/test, hot replacement, real reproduction and
one targeted regression. Release requires source verification, artifact builds,
SHA-256 checks, x86 ISO installation, ARM installer structure and upgrade
structure. VMware/TAP measurements never represent production performance.
