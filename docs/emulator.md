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

路由合成测试只接受 NeedsLogin 且 table 52 为空的环境，避免覆盖实际账号路由；它不提供报文转发能力。两个 Go 测试程序交叉编译到 `.work/tests` 后推入同一模拟器，以 `-test.run '^TestAndroid' -test.v` 执行，不能在主机执行。

切换启动环境前先停止模块、停止测试 VPN 并执行 `sync`，再关闭模拟器。共用同一 AVD 和状态目录，不克隆 AVD，不创建数据备份。测试完应删除一次性负向测试 ZIP、device 临时工具和过期构建产物。
