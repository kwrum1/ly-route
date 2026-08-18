# 开发与编译源码边界

本规则用于避免编译机残留目录被误当成当前源码，产生“类型不存在”等假错误。
它只约束编译机同步和发布构建，不是每次热修复的前置门禁；日常流程见
[开发、热修复与验收流程](development-workflow.md)。

## 唯一有效源码

- Ly Route 后端只能从仓库根目录的完整 `backend/` 编译。
- 向编译机同步后端时，必须整体替换 `backend/`，不能只覆盖 `backend/internal/runtime/vpp/` 或其他子目录。
- `backend/internal/runtime/vpp/` 是 Ly Route 当前后端的一部分，不能独立作为 Go 模块或单独编译。

## VPP 上游源码

- `vpp-master/`、`govpp-master/` 和 `build/vpp-*` 都是临时第三方源码或构建缓存，不是 Ly Route 后端源码。
- 无法核对提交号的第三方源码目录视为过期目录，必须清理后重新获取。
- 构建 VPP 包时必须显式设置 `LY_ROUTE_VPP_SRC`，构建脚本不得静默回退到仓库中的旧目录。

## 编译机同步流程

1. 核对目标必须是编译机仓库下的完整 `backend/`。
2. 删除旧 `backend/` 后整体同步当前工作区的 `backend/`。
3. 执行 `bash scripts/normalize-source-line-endings.sh /root/ly-route`。
4. 执行 `bash scripts/verify-compiler-environment.sh`，确认固定 Go 工具链和环境。
5. 在 `backend/` 内执行 `go test -run '^$' ./...`，确认所有包可编译。
6. 再构建 `./cmd/gateway-control`；不得从 VPP 子目录直接构建。
7. 第三方 VPP 源码和插件构建缓存用完即清理，避免影响下一轮。

如果目标目录只剩 `internal/runtime/vpp` 等局部内容，应判定为同步失败，先清理再整体同步，不能继续补文件碰运气。

工具链、CRLF/LF 和 Windows 到 Linux 同步的完整边界见
[`compiler-build-environment.md`](compiler-build-environment.md)。

## 本机文件保留边界

本地工作区只能长期保留以下内容：

1. 唯一有效源码仓库 `ly-route-github/`；
2. SSH、同步和 ESXi 操作所需的小型工具目录；
3. 当前一轮验收夹具，以及从被测机回存并带 SHA-256 的唯一运行版本快照。

下列内容全部属于可再生产物，完成当前命令后必须清理，禁止积累多个版本：

- ISO、IMG、QCOW2、VMDK、DEB、SO、升级包和 rootfs 压缩包；
- QEMU 安装包、第三方固件缓存、下载后的 VPP/Armbian 源码包；
- `build/`、`dist/`、`runtime-debs/`、`.codex-build/`、`.tmp*/` 和演示环境 `artifacts/`；
- 带 `fix`、`v2`、`final`、`bootstrap` 等后缀的历史控制程序和插件；
- 远程编辑副本、局部后端副本、旧 UI 副本、串口日志、旧截图和历史 CI 日志。

当前运行程序需要回滚时，只允许保留一套从被测机直接导出的快照，并记录控制程序、前端资源和全部自研 VPP 插件的 SHA-256。新版本通过验收后覆盖该快照，不追加版本号副本。

大型第三方缓存不得提交到 Git。CI 必须按版本和校验和下载依赖，或使用 CI 平台缓存；`third_party/ophub-cache/` 等目录只能临时存在。若历史提交已经包含大型固件，不得在有未提交源码时直接改写远端历史；本机可使用与当前远端提交一致的浅层元数据减少空间占用。
