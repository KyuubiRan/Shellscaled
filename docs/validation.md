# 验证记录

日期：2026-10-27。运行目标仅为 `Medium_Phone` Android Emulator（ARM64）；未触碰实体设备。

## 已观察的环境

- 原 AVD 为 Google APIs Android 37.1、16 KB 页，支持 `adb root`，SELinux enforcing。
- 为测试 KernelSU，使用项目私有启动覆盖文件运行官方 4 KB `6.12.58-android16-6` 内核及匹配设备模块；SDK 镜像未改写。
- KernelSU `v3.3.1` 已实际加载，内核接口报告版本 32680、LKM 模式，用户态组件和 Manager 已安装。
- 4 KB 启动后 Android 完成开机，Wi-Fi / 以太网接口正常。该测试环境的准备文件及来源校验清单集中于忽略的 `.work/ksu`，约 49 MB。

## 已完成的模拟器验证

| 项目 | 结果 |
| --- | --- |
| Tailscale 1.104.2 ARM64 CGO / PIE 构建 | 本机构建，模拟器报告相同版本与源码提交 |
| KernelSU 3.3.1 离线安装和开机启动 | 4 KB 内核上通过 |
| Magisk 31.1 离线安装和开机启动 | 原始 16 KB 内核上通过 |
| 两种 root 环境的服务烟雾测试 | 各 7 项通过 |
| Android 路由谓词、无效用户查询、FIB 消息解析及双栈事件订阅测试 | 交叉编译后仅在模拟器执行，通过 |
| 损坏载荷 / 错误架构 | 安装被正确拒绝，原模块继续运行 |
| 日志限额 | 两份日志各追加超过 1 MiB，监督进程均收敛至 64 KiB，daemon 保持运行 |
| IPv4 / IPv6 策略规则删除恢复 | 分别删除本模块精确规则后自动恢复 |
| 停止后的规则清理 | IPv4 / IPv6 本模块策略规则移除 |

每组烟雾测试覆盖：start/status 幂等、私有目录权限与 socket、重复 stop 和状态保留、伪造 PID 不能误杀无关进程、daemon 被杀后的恢复、disable 行为、pending remove 行为。测试没有认证或清除账号状态。

`tests/network-policy.sh` 在真实 Android VpnService 存在时添加临时合成目标路由，已验证 IPv4 / IPv6 Tailnet 目标走 `tailscale0`、带 bypass mark 的外层传输选择底层网络、普通互联网目标仍走 VPN，并确认重启 daemon 不撤销 VpnService。测试结束清除夹具和临时路由。这是 FIB / 生命周期验证，**不能替代登录后的加密报文交付验证**。

首次离线安装发现管理器不提取 `META-INF`，已将校验清单限定到实际安装的载荷并重测两种管理器。首次 Magisk 镜像启动缺少用户态工具，通过官方 `fix_env` 初始化完成，未写 boot 分区。

后续专门的 IPv6 删除测试发现上游只订阅 IPv4 rule 事件，且误用 route 消息解析 FIB rule。已修复双栈订阅、FRA_PRIORITY / FRA_TABLE 解码与 32 位表编号处理；模拟器回归确认 IPv6 恢复生效，不再因 Android UID_RANGE 属性刷解析错误日志。

SELinux 全局保持 enforcing。root 进程使用各管理器提供的域；KernelSU 默认 root 域的许可范围不等同于普通应用域，未额外将系统切到 permissive。

## 登录后的剩余验收

实际 Tailnet IPv4 / IPv6 双向数据传输、现有 VPN 同时工作的真实连通性、网络切换 / 重连、登录状态的重启和更新保留仍待完成。VPN lockdown、出口节点、子网转发和热点不属于已经验证的能力。

需要账号登录时通知所有者；没有回复则暂停 goal。不得使用已有私人账号状态或凭证代替授权。

## 当前交接点

- 源码提交：`c322e8f5085378be2342f64648382de6dfdd75ad`，主仓库和 submodule 均在 `dev`；源码提交已推送，父仓库固定该提交。
- 最终构建在 Magisk / 16 KB 和 KernelSU / 4 KB 环境均完成双栈规则恢复与 VpnService 路由复测。
- 模拟器已切回 KernelSU，服务运行，等待手动账号授权；测试 VPN 已停止。
- 包：`dist/Shellscaled-1.104.2-r0-arm64.zip`，SHA256 `f29ec3403a0252c6a565975cee17db75c67e7fe496585e96668ca3ff4bef451d`。
- 两个 GitHub 仓库均为 private，Actions disabled，检查到运行数为 0。`main` 尚未提升到这一候选构建，未创建正式 release。
- 已删除约 1.4 GB 编译缓存、暂存载荷和一次性负向测试包；保留单份工具链 / 依赖缓存、必要启动覆盖、源码和最终 ZIP，集中于项目忽略目录。

恢复工作时先检查现有模拟器、KernelSU / daemon 状态和认证结果，不重复初始化账号或重新下载整套环境。若登录链接已失效，重新运行模块 CLI 的 `login` 并通知所有者。
