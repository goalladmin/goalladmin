# 文档导航

这些文档对应当前源码。使用发布版本时，请在对应 Git 标签下阅读文档；已发布功能和升级记录以 [CHANGELOG](../CHANGELOG.md) 为准。

## 按任务阅读

| 你要做什么 | 从哪里开始 |
| --- | --- |
| 本机运行并登录 | [中文 README](../README.md#快速开始) / [English README](../README.en.md#quick-start) |
| 开通代理商或商户、分配员工权限 | [后台使用指南](guides/admin-guide.md) |
| 开启或关闭入驻、使用商户邀请 | [后台使用指南：入驻与邀请](guides/admin-guide.md#入驻与邀请) |
| 配置 IP 白名单和黑名单 | [后台使用指南：ip-访问控制](guides/admin-guide.md#ip-访问控制) |
| 部署三个独立域名 | [三端部署指南](guides/portals-deployment.md) |
| 同一端运行多个实例、配置 Redis | [多实例部署指南](guides/multi-instance.md) |
| 升级、备份、恢复和查看日志 | [运维指南](guides/operations.md) |
| 处理无法访问、登录、刷新或权限问题 | [常见问题与排障](guides/troubleshooting.md) |
| 选择密码哈希算法 | [密码哈希算法指南](guides/password-hash.md) |
| 添加业务表、接口和页面 | [新增模块指南](guides/new-module.md) |
| 修改框架本身 | [技术规范](spec.md)、[决策记录](decisions.md)、[协作规则](../CLAUDE.md) |

## 参考资料

| 文档 | 维护范围 |
| --- | --- |
| [技术规范](spec.md) | 认证、授权、主体隔离、数据库、配置、安全边界、测试和扩展契约 |
| [接口清单](api.md) | 路由、守卫、参数、响应、分页与错误码 |
| [开发约定](conventions.md) | 数据表、更新字段、前端依赖、国际化和文档维护 |
| [决策记录](decisions.md) | 设计选择及其原因；历史条目保留，后续条目可以更新先前选择 |
| [配置示例](../server/config/config.example.yaml) | 完整配置键、默认值和环境变量映射 |
| [Compose 环境变量示例](../deploy/.env.example) | 容器示例使用的变量；每份 Compose 实际传入的变量可能不同 |
| [Makefile](../Makefile) | 本地开发、构建和验证命令的实际定义 |

安装和维护操作由部署者在目标环境执行。示例地址、主体名称和凭据占位符需要替换；测试数据库不用于保存业务数据。命令中的“平台程序”指 `server/main.go` 编译的后端或对应容器，不是平台后台网页。
