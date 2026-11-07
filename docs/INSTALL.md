# 安装与升级

适用版本：**1.2**

## 1. 支持平台

| 平台 | 安装方式 |
|---|---|
| x86-64 | ISO 整机安装（安装镜像内置系统与全部组件） |
| ARM64 | 在已安装的 Armbian Bookworm（arm64）系统上一键安装 |

设备至少需要两个网口：

- **管理口**：保留在系统内，用于登录 Web 控制台和日常维护，不参与转发。
- **数据口**：其余网口由转发引擎自动接管，作为 WAN / LAN 使用。

> ISO 安装会清空所选磁盘，请提前备份数据；升级包安装不会清空磁盘。

## 2. x86-64：ISO 安装

1. 下载 `ly-route-gateway-gui-installer.iso`。
2. 用 Rufus、balenaEtcher 或 `dd` 制作 U 盘启动盘。
3. 目标设备选择 U 盘启动（BIOS 或 UEFI 均可）。
4. 在安装向导中依次完成：
   - 选择安装磁盘（整盘，建议不小于 8 GiB，数据会被清空）；
   - 选择管理网口；
   - 设置管理地址：DHCP 自动获取，或填写静态 IP / 掩码 / 网关 / DNS；
   - 设置管理员密码。
5. 开始安装，完成后设备自动重启，此时可以拔出 U 盘。
6. 将电脑接入管理口，浏览器访问：

```text
https://<管理地址>/
```

## 3. ARM64：一键安装

适用于已安装好的 Armbian Bookworm（arm64）系统，保留原有内核、DTB 与引导。

```bash
tar --use-compress-program=unzstd -xf ly-route-gateway-arm64-installer.tar.zst
cd ly-route-gateway-arm64-installer
sha256sum -c checksums.sha256
sudo ./install.sh
```

安装说明：

- 需要临时联网安装系统依赖；安装脚本可重复执行，不会覆盖已有的
  `/etc/ly-route/runtime.env` 与 `/etc/ly-route/appliance.env`。
- 默认管理地址为 `https://192.168.88.254/`，可在安装后于「配置管理」中修改。
- 初始管理员密码保存在 `/root/ly-route-initial-admin-password`，首次登录必须修改。

## 4. 首次登录

1. 浏览器打开 `https://<管理地址>/`。
2. 控制台使用自签名证书，浏览器会提示“不安全”，选择继续访问即可。
3. 使用账号 `admin` 和初始密码登录，并按提示设置新密码。
4. 建议按以下顺序完成初始化配置：
   - 网卡设置：确认管理口与数据口，设置接口方向；
   - WAN 线路：创建拨号或静态线路；
   - LAN 线路：设置内网地址与 IPv6；
   - DHCP 服务：为 LAN 下发地址；
   - 策略路由：至少添加一条默认上网策略；
   - 点击「应用配置」，等待状态变为“已应用”。

## 5. 升级

### 通过界面升级

1. 进入「系统 → 固件升级」。
2. 上传升级包（`.tar.zst` 或 `.zst`）。
3. 等待校验通过后点击安装。
4. 升级过程中控制服务会重启，页面显示“系统重启中”，稍后自动恢复。

升级只替换系统与程序文件，**保留业务配置和管理地址**。

### 通过 API 升级

```bash
# 登录并保存会话
curl -k -c /tmp/ly.cookie -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"密码"}' \
  https://管理地址/api/v1/auth/login

# 上传并校验升级包
curl -k -b /tmp/ly.cookie \
  -F firmware=@ly-route-upgrade-gateway-bookworm-amd64.tar.zst \
  https://管理地址/api/v1/firmware/update/stage

# 开始安装
curl -k -b /tmp/ly.cookie -H 'Content-Type: application/json' \
  -d '{"confirm_install":true}' \
  https://管理地址/api/v1/firmware/update/install
```

### 升级后检查

- 「系统状态」中组件健康为“全部正常”；
- 「概览」中 WAN 线路在线、流量正常；
- 管理员账号可以正常登录；
- 如需命令行确认：

```bash
/usr/lib/ly-route/ly-route-control --version
curl -ks https://127.0.0.1/api/v1/health
```

## 6. 备份、恢复与回滚

- **日常备份**：「配置管理 → 导出配置」保存 JSON 文件；重要变更前创建快照。
- **升级回滚**：升级时自动保留上一版本程序与配置备份，安装失败会自动恢复到升级前状态。
- **恢复出厂**：「配置管理 → 配置初始化」，输入 `RESET` 确认；执行后需要重新配置并应用。
- **整机重装**：使用 ISO 重新安装后，在「配置管理 → 导入配置」恢复备份并应用。
