# 升级与回滚

## 支持范围

- 1.1.x → 1.1.x：支持原地升级；
- 1.0.x → 1.1.x：支持，但升级前请先做配置备份；
- 跨大版本降级：不支持，请用对应版本 ISO 重装后恢复配置。

## 方式一：Web 控制台

1. 下载与架构匹配的升级包：
   - x86_64：`ly-route-upgrade-gateway-bookworm-amd64.tar.zst`
   - ARM64：`ly-route-upgrade-gateway-bookworm-arm64.tar.zst`
2. 登录控制台，进入 **系统 → 固件升级**；
3. 上传升级包，确认版本与校验值；
4. 执行安装，等待服务重启；
5. 刷新页面，确认版本与健康状态。

## 方式二：API

```bash
# 上传并暂存
curl -b cookies -X POST https://<gateway>/api/v1/firmware/update/stage \
  -F firmware=@ly-route-upgrade-gateway-bookworm-amd64.tar.zst

# 确认安装
curl -b cookies -X POST https://<gateway>/api/v1/firmware/update/install \
  -H 'Content-Type: application/json' -d '{"confirm_install":true}'
```

## 升级会保留什么

- `/etc/ly-route/runtime.env`；
- `/etc/ly-route/appliance.env`；
- 业务配置、策略、地址与密钥材料；
- 上一版本二进制与配置备份。

## 升级后检查

```bash
/usr/lib/ly-route/ly-route-control --version   # 应为目标版本
curl -sk https://127.0.0.1/api/v1/health
systemctl status ly-route-control-api vpp ly-route-pingora
```

数据口应保持 `takeover_mode=dpdk_uio`；管理口应仍在 Linux。

## 回滚

- 升级安装会保留上一版本；失败时 `ly-route-recovery.service` 自动重放上一已提交代。
- 手动回滚：在控制台 **系统 → 固件升级** 选择上一版本，或使用上一版本升级包重新安装。
- 严重故障：使用对应版本 ISO 重装，然后从配置备份恢复。

## 注意事项

- 升级过程中不要断电；
- 升级前确认管理口可达，避免升级后无法登录；
- 自定义的 `/etc/ly-route` 之外的文件可能被覆盖，请自行备份；
- 升级包必须与架构匹配，x86_64 与 ARM64 包不可混用。
