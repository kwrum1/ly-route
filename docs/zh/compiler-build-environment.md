# 编译机工具链与换行边界

这份规则用于发布构建和明确的编译排障，避免把时间浪费在 Go 自动升级、主机环境漂移和
Windows 换行符导致的假故障上。它不是每次热修复的前置门禁；日常流程见
[开发、热修复与验收流程](development-workflow.md)。

## 固定基线

编译机和 CI 使用同一基线文件：
`config/build/compiler-toolchain.env`。

当前基线为：

- Go `go1.24.4`
- Linux `x86_64` 编译机，Debian Bookworm 为目标 rootfs
- `GOTOOLCHAIN=local`，禁止 Go 自动下载或切换工具链
- `CGO_ENABLED=1`、`gcc`、`g++`
- `GOPROXY=https://goproxy.io,direct`、`GOSUMDB=off`
- 构建时区 `UTC`，文本文件统一 `LF`

发布构建或专门的构建排障开始前执行：

```bash
bash scripts/verify-compiler-environment.sh
```

发布构建检查失败时停止构建，不能用“换个 Go 版本再试”掩盖环境漂移。需要升级工具链时，
先修改基线文件、CI 和编译机，再单独验证，不在一次构建中临时切换。

## Windows → Linux 同步

仓库 `.gitattributes` 对源码、脚本、配置、文档统一声明 `eol=lf`。但通过 SFTP、
压缩包或临时目录同步时不能只依赖 Git 属性，必须在编译机完整源码同步后执行：

```bash
bash scripts/normalize-source-line-endings.sh /root/ly-route
bash scripts/verify-compiler-environment.sh
```

规范化脚本只处理已列出的文本扩展名，并排除 `.git`、`dist`、构建缓存、下载的
VPP 上游源码和二进制产物，不会修改固件、数据库或第三方二进制文件。

## 固定同步顺序

1. 校验 `/root/ly-route` 是目标仓库的真实路径。
2. 整体替换仓库中的 `backend/`，禁止只同步 `internal/runtime/vpp/`。
3. 运行换行规范化脚本。
4. 运行编译环境门禁。
5. 在 `backend/` 执行 `go test -run '^$' ./...`。
6. 构建 `./cmd/gateway-control` 或目标命令。

不得在残留的旧 VPP 子目录中直接运行 Go 编译。若出现大量“类型不存在”，先检查
源码树完整性、Go 版本和 CRLF，再判断是否是真实代码错误。
