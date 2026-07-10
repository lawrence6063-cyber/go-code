// Package sandbox 约束命令执行与文件操作的安全边界，是「不依赖模型善意」的确定性防线。
//
// 它由两层构成：
//   - 纯函数防线（path.go）：路径越界校验（ValidatePath，含 EvalSymlinks 解析软链接后再判越界）、
//     控制面写禁止（IsControlPlaneWrite，禁写 .cogent/.git）、危险命令识别（IsDangerousCommand）、
//     命令目标控制面校验（IsControlPlaneCommandTarget，堵住 bash 用重定向/rm/mv 等绕开
//     IsControlPlaneWrite 的缺口，与 write_file/edit_file 的控制面拦截同等强度）。
//   - 命令执行纵深（sandbox.go）：命令路由（ShouldSandbox）+ 危险命令确定性拦截 +
//     控制面目标拦截（ErrControlPlaneCommand）+ 受限环境执行（精简 PATH、HOME 指向 WorkRoot、
//     不透传宿主密钥）+ 工作目录约束 + 超时 + 执行后清理。
//
// 安全姿态与边界（OPTIMIZE_SPEC S4/S6，显式声明可审计的取舍）：
//   - 采用策略型隔离 + 路径/命令确定性校验 + 执行后清理，不依赖平台特定的 OS 级强隔离
//     （Linux landlock/seccomp、bwrap）——这是与「单二进制本地工具」定位匹配的工程取舍；
//     进阶可在 Linux 接 landlock（限制 fs 访问）/seccomp（限制 syscall）作为可选加固层。
//   - 残余风险：受限环境本身不限制 bash 发起的网络连接（sandbox 层无网络层拦截）。
//     v1 引入 web_fetch 工具面后已单独为其实现 SSRF 防护（tool/webfetch.go：DNS 解析级
//     地址校验 + 直连已校验 IP + 重定向复检），但该防护只覆盖 web_fetch 这一条出网通道，
//     bash 仍可用 curl/wget 等直接访问任意地址（含内网）——这是显式记录、尚未收敛的
//     已知不对称，后续如需收窄可在 sandbox 层加统一的网络出站校验。
//   - 验收脚本 / git 操作刻意以 Config{Enabled:false} 继承宿主 PATH（调用 go/git 工具链）：
//     它们属开发者可信控制面（非模型直接发起），即便如此，危险命令拦截与 WorkRoot 约束仍无条件生效，构成纵深。
package sandbox
