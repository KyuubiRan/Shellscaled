# Shellscaled

在 root Android 上直接运行 Tailscale，不申请 Android `VpnService`。目标是让 Tailnet 与已有 Android VPN 共存，同一离线模块兼容 Magisk 和 KernelSU。

当前开发基线为 **Tailscale 1.104.2**。开发分支为 `dev`，`main` 保留发布基线；完成模拟器验收后才提升发布分支。当前还没有经过登录后连通性验收的正式发布。

## 安装与操作

在 Magisk / KernelSU Manager 中安装对应架构的 ZIP，重启后使用完整路径操作。模块不依赖系统文件挂载或 KernelSU metamodule。

```sh
su -c /data/adb/modules/shellscaled/ctl.sh status
su -c /data/adb/modules/shellscaled/ctl.sh restart
su -c '/data/adb/modules/shellscaled/bin/tailscale login'
su -c '/data/adb/modules/shellscaled/bin/tailscale status'
su -c '/data/adb/modules/shellscaled/bin/tailscale ip'
```

安装过程不联网、不启动服务、不登录。若旧的 `magisk-tailscaled` 已启用，应先停用并重启，再安装本模块；本模块不会擅自移动旧模块账号状态。

默认使用原生 `tailscale0`、`accept-dns=false`、`netfilter-mode=off`。Android 的系统 DNS、Private DNS 与当前 VPN 的 DNS 仍由 Android 管理；暂不提供系统 MagicDNS 接管。此构建关闭 Tailscale SSH 服务端与二进制自更新，更新统一通过模块进行。

账号状态位于 `/data/adb/shellscaled/state`，日志位于 `/data/adb/shellscaled/logs`。升级保留状态，卸载会在停止本模块进程后删除本模块数据。日志受限额管理，不创建逐版本备份。

```sh
su -c '/data/adb/modules/shellscaled/ctl.sh logs daemon'
su -c '/data/adb/modules/shellscaled/ctl.sh logs service'
su -c '/data/adb/modules/shellscaled/ctl.sh stop'
su -c '/data/adb/modules/shellscaled/ctl.sh start'
```

停用或标记移除后，监督进程会停止服务。手动 `stop` 保持停止，直到显式 `start` 或下一次开机。日志可能包含账号、网络或登录信息，分享前需检查。

## 本地构建

需要 Python 3、源码 `go.mod` 指定版本或更新的 Go、已安装的 Android NDK。私有 submodule 需要对应 GitHub 访问权限。

```sh
git clone --recurse-submodules https://github.com/KyuubiRan/Shellscaled.git
cd Shellscaled
git switch dev
git submodule update --init
python3 scripts/build.py --arch arm64
```

可用 `ANDROID_NDK_HOME` 指定已有 NDK，用 `SHELLSCALED_GO` 指定已有 Go 可执行文件。构建只复用工具链，不自动下载 SDK / NDK；Go 依赖下载继承环境代理，并缓存在 `.work/cache`。构建、准备和测试文件集中在 `.work`，引用项目放 `.reference`，离线 ZIP 与校验文件放 `dist`。

版本直接读取 `tailscale/VERSION.txt`；`versionCode = major × 100000000 + minor × 100000 + patch × 100 + revision`。同一个 Tailscale 版本的模块修订可使用 `--revision 1`，显示版本仍与 Tailscale 一致。ARM / x86_64 构建入口已预留，尚未获得相应模拟器运行验证。

GitHub 仓库保持 private，Actions 已禁用，预留工作流不执行编译。未经明确授权不启用托管构建。

## 验证边界

所有运行测试仅限 `Medium_Phone` 模拟器，使用 `scripts/emulator.py` 校验 AVD 名称后再调用 adb。构建不等于运行验收；登录后还需验证 Tailnet IPv4 / IPv6、VPN 共存、网络重连和重启持久性。

测试夹具位于 `tests/vpn`，由 `scripts/build-vpn-fixture.py` 使用已有 Android SDK 编译。它是真正的 Android VpnService，但会主动丢弃所声明路由的流量，仅用于模拟器验证策略路由，不能当作日常 VPN 客户端。

详见 [Android 网络调研](docs/android-network.md) 和 [验证记录](docs/validation.md)。VPN lockdown、厂商自定义策略、其他 root 代理模块、热点转发、出口节点及子网转发尚未承诺兼容。

## 来源与许可

源自 [anasfanani/magisk-tailscaled](https://github.com/anasfanani/magisk-tailscaled) 的使用场景和 BSD 许可；Android 移植参考 [anasfanani/tailscale-android-cli](https://github.com/anasfanani/tailscale-android-cli)，核心基于 [Tailscale](https://github.com/tailscale/tailscale)。保留各自版权与专利声明。Shellscaled 是第三方项目。
