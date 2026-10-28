# 验证记录

日期：2026-10-27～28。运行目标仅为 `Medium_Phone` Android Emulator（ARM64）；未触碰实体设备。Tailscale 源码固定为 `c322e8f5085378be2342f64648382de6dfdd75ad`，版本 1.104.2。

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

## 已认证的网络验收

所有者于 2026-10-28 手动完成登录。测试使用同一个已在线 peer，未扫描网络、操作远端文件或执行远端 shell。私有地址和完整状态只存放在忽略的本地测试目录，不写入本记录。

| 场景 | 结果 |
| --- | --- |
| 无测试 VPN | Tailscale ping、系统 ICMP、普通 PeerAPI TCP 请求通过，覆盖 Tailnet IPv4 / IPv6 |
| 全流量黑洞 VpnService 同时启用 | Tailnet 双栈 ICMP / TCP 仍通过，普通互联网 TCP 按夹具设计失败 |
| 停止测试 VPN | 互联网通信恢复，Tailscale 保持 Running |
| Wi-Fi 关闭约 2.3 秒，再重新开启 | 模拟器回退备用虚拟网络，恢复默认 Wi-Fi 后同 peer 双栈 ICMP / TCP 通过 |

Wi-Fi 切换中的备用承载由模拟器内核虚拟网络提供，即便 Android 框架显示 CELLULAR，也不视为真实蜂窝网络验收。没有使用实体设备或 VPN lockdown。出口节点、子网转发、热点、厂商网络策略和其他 root 代理组合仍未验证。

## WebUI 与状态回归

- 真实 KernelSU WebView 已加载本地 CSP 页面，状态查询、开关、命令提交和保存实际工作。
- 命令输入通过 base64 JSON 传给固定原生 helper，由 `exec.CommandContext` 直接执行固定二进制和白名单参数，不经过 shell。19 组命令逃逸、其他程序、任意 socket / 文件路径和危险子命令均被拒绝，副作用哨兵未产生。
- 原生参数边界、严格 RPC 解码、偏好局部修改、敏感字段不返回、输出限额等四组 Go 测试交叉编译后在模拟器运行通过。
- 连接 / 断开、外部模块按钮反复切换后，账号身份和原设置保持，模块说明即时对应连接状态。设置测试结束恢复原值。
- 修复了两个实际回归：状态更新竞争，以及 KernelSU 隐藏 sysfs 导致说明写到错误来源。现使用进程间锁，在锁内读取新状态，同时刷新本模块的 KSU 临时覆盖和 module.prop 回退。
- 修复设置页草稿覆盖其他字段：先保留一个未保存开关，通过命令页改变另一个字段，再返回保存；真实 WebView 验证两者都保留。
- 账号状态在升级与重启后保持；测试未清除或替换登录状态。

## 发布与复现

- 包：`dist/Shellscaled-1.104.2-r1-arm64.zip`，显示版本 `1.104.2`，`versionCode=110400201`。SHA256 见同目录 `.zip.sha256`；构建提交记录在 ZIP 内的 `build-info.json`。
- 主仓库和 source submodule 使用 `dev` 开发、`main` 发布；源仓库先推送，再推送父仓库引用。仓库保持 private，Actions disabled，编译仅在本机执行。
- 生命周期、双栈规则与实际 VPN 测试分别验证，不能用构建成功或 FIB 查询替代实际报文测试。复现方式见 [模拟器环境](emulator.md)。
- 旧构建、暂存载荷和一次性测试工具不逐版本保留。保留单份必要工具链 / 依赖缓存、启动覆盖、源码和最终 ZIP，集中于项目忽略目录。

以后若需要重新登录，仍应通知所有者；无人回复时暂停 goal，不读取其他私人凭证或账号状态替代登录。
