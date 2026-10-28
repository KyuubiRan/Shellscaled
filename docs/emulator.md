# 模拟器测试环境

只允许 `Medium_Phone`。所有 adb 操作使用 `python3 scripts/emulator.py <adb arguments>`；该工具同时验证 emulator 序列号和 AVD 名称，不会默认选择连接中的手机。

原 AVD 为 Android 37.1 Google APIs ARM64、16 KB 页。常规启动使用 `emulator @Medium_Phone`，可 `adb root`。不能直接将通常按 4 KB 页编译的官方 KernelSU LKM 加载到这一 16 KB 内核：loader 可能改写 vermagic，而模块中的页表计算仍取决于编译时页大小。

## KernelSU 对照环境

测试准备只下载 [官方 qemu-kernel](https://android.googlesource.com/platform/prebuilts/qemu-kernel/+/7fe7e19e1bdb1850d637ce32b206db0b61fc937e/arm64/6.12/) 中必要的 4 KB 内核与模块单文件，固定提交 `7fe7e19e1bdb1850d637ce32b206db0b61fc937e`。没有 clone AOSP。匹配的 GKI / goldfish 模块按依赖合入私有 ramdisk，避免混用 SDK vendor 中的 16 KB 模块。

加入 [KernelSU v3.3.1](https://github.com/tiann/KernelSU/releases/tag/v3.3.1) 的 ksuinit 与匹配 LKM，按照官方 init / init.real 接力方式启动。测试镜像的 allow-shell 设置只服务于这个可 root 的模拟器，不能作为实体设备分发镜像。

```sh
emulator @Medium_Phone \
  -kernel "$PWD/.work/ksu/kernel-4k.gz" \
  -ramdisk "$PWD/.work/ksu/ramdisk-4k-ksu.img" \
  -no-snapshot -show-kernel
```

首次进入后安装同版 Manager，并执行 `ksud install` 初始化用户态组件。生成的来源 / 校验清单保存在 `.work/ksu/SOURCES.json`；必要资产约 49 MB，不改写 SDK 或 AVD 配置。

## Magisk 对照环境

使用 [官方 Magisk v31.1](https://github.com/topjohnwu/Magisk/releases/tag/v31.1) 与原 SDK 16 KB 内核。原 ramdisk 是多段 CPIO，需完整合并，保留元数据后按官方 [boot_patch.sh](https://github.com/topjohnwu/Magisk/blob/v31.1/scripts/boot_patch.sh) 的 CPIO 逻辑嵌入 Magisk。CPIO 内部保存原 init 用于接力，这是启动结构的一部分；不创建额外整机 / SDK 备份。

```sh
emulator @Medium_Phone \
  -ramdisk "$PWD/.work/magisk/ramdisk-magisk-16k.img" \
  -no-snapshot -show-kernel
```

此命令不带 KernelSU 的 4 KB `-kernel` 参数。Magisk 在该 AVD 的可执行入口是 `/debug_ramdisk/magisk`，未必在 adb shell 的 PATH 中。

首次启动需要提取 APK 当前 ABI 的 native 工具及安装脚本，按官方 [MagiskInstaller](https://github.com/topjohnwu/Magisk/blob/v31.1/app/core/src/main/java/com/topjohnwu/magisk/core/tasks/MagiskInstaller.kt) 的 `fixEnv` 路径初始化 `/data/adb/magisk`，再执行 `env_check 31.1 31100`。仅修复用户态工具，不调用写 boot 分区的 `direct_install`。

## 执行验证

```sh
python3 scripts/build-device-tests.py
python3 scripts/build-vpn-fixture.py
python3 scripts/emulator.py install -r .work/vpn-fixture/vpn-fixture.apk
python3 scripts/emulator.py push tests/smoke.sh /data/local/tmp/shellscaled-smoke.sh
python3 scripts/emulator.py shell sh /data/local/tmp/shellscaled-smoke.sh
python3 scripts/emulator.py push tests/network-policy.sh /data/local/tmp/shellscaled-network-policy.sh
python3 scripts/emulator.py shell sh /data/local/tmp/shellscaled-network-policy.sh
```

路由合成测试只接受 NeedsLogin 且 table 52 为空的环境，避免覆盖实际账号路由；它不提供报文转发能力。Go 测试程序交叉编译到 `.work/tests` 后推入同一模拟器，不能在主机执行。Android 路由和用户查询以 `-test.run '^TestAndroid' -test.v` 执行，netmon 使用相应双栈 FIB 测试过滤器。

已经登录后可执行 WebUI 后端验收；它恢复原设置并保持连接，不清除账号：

```sh
python3 scripts/build-device-tests.py --web-only
python3 scripts/emulator.py push .work/tests/web-control.test /data/local/tmp/shellscaled-web-control.test
python3 scripts/emulator.py shell chmod 755 /data/local/tmp/shellscaled-web-control.test
python3 scripts/emulator.py shell 'su -c "TMPDIR=/data/local/tmp /data/local/tmp/shellscaled-web-control.test -test.v"'
python3 scripts/test-webui.py --manager ksu
# 切换到 Magisk 启动环境后使用 --manager magisk
```

真实 KernelSU WebView 还需手动 / UI 自动化验证：开关即时写入且保留其他字段；写入及后续状态确认都失败时，开关保持禁用，直到读到有效状态。不要将完整 status / prefs、登录链接或带账号地址的截图纳入版本库。

切换启动环境前先停止模块、停止测试 VPN 并执行 `sync`，再关闭模拟器。共用同一 AVD 和状态目录，不克隆 AVD，不创建数据备份。测试完应删除一次性负向测试 ZIP、device 临时工具和过期构建产物。

## WebView 与三语言回归

`python3 scripts/build-webui-fixture.py` 使用已有 SDK 构建隔离的测试 APK。
在模拟器安装并启动 `dev.shellscaled.webuitest/.MainActivity`，读取
`ShellscaledUiTest` 日志的最新 `START` 到 `DONE pass=29 fail=0`。
它在真实 Android WebView 中加载原样资源，使用无 root、无网络权限的
内存桥验证前端，不访问生产账号或 socket。详见 [夹具说明](../tests/webui/README.md)。

这不能代替真实 KernelSU 桥接验收。生产 helper 的 locale、命令边界与
状态说明由 `scripts/test-webui.py` 在实际模块上验证。DERP TLS 回归程序为
`.work/tests/derphttp.test`，交叉编译后在模拟器以
`-test.run '^TestAndroidDERP' -test.v` 执行，覆盖坏 IPv6 回退、健康 IPv6、
证书拒绝及调用方 deadline/连接关闭。

完整 `build-device-tests.py` 也生成 `.work/tests/taildrop.test`。推送后以
`TMPDIR=/data/local/tmp` 在模拟器运行全部包内测试，无需登录或访问真实收件目录。
构建的 `ts_omit_ssh` 标签排除依赖 SSH 的独立双节点集成 harness，保留
Taildrop 的接收、恢复、文件校验及并发测试。已有 Go 版本不足时，通过
`SHELLSCALED_GO` 指向满足 `tailscale/go.mod` 的已安装工具链。

`tests/control-lock.sh <payload-dir>` 与 `tests/cli-entry.sh <payload-dir>`
使用临时目录验证锁和全局 CLI 行为。通过 guard 推送实际模块脚本及夹具；
注意把测试的 `cli-entry.sh` 另命名为 `cli-entry-test.sh`，避免覆盖
payload 中同名的生产入口。它们不操作真实服务、账户、管理器目录或挂载。


全局命令的 root 场景必须从 UID 2000 的普通 adb shell 验证，而不是只检查
已 `adb root` 的环境。`scripts/emulator.py unroot` 仍会校验 AVD；确认
`su` 已获模拟器的 root 授权后，分别验证交互式 `su` 与 `su -c` 的
`tailscale version`、`tailscale status`、`tailscaled --version` 及幂等启动。
测试完成可通过同一 guard 恢复 `adb root`。授权仅是本地模拟器准备，
模块本身不会修改任何设备的 superuser 授权策略。

Google API 镜像的 `adb root` 环境可能直接命中镜像自带的调试 `su`，其语法
与 KernelSU 的 `su -c` 不同。因此 root-shell 入口验收先用 guard `unroot`
恢复 UID 2000，再调用管理器提供的 `su`。ADB 已为 UID 0 时，设备管理
操作直接执行即可，不需要再套一层调试 `su`。
