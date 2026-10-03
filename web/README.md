# web

前端工作区。`packages/shell` 是三个端共用的框架壳 `@ga/shell`（业务方不改），`apps/<端>` 是各端应用：`apps/platform` 平台端、`apps/agent` 代理商端、`apps/merchant` 商户端（D-064）。

统一使用 pnpm，`packageManager` 在 `package.json` 中固定版本（当前 10.28.0）。`pnpm-lock.yaml` 是可复现安装的依据，CI、Docker 镜像和正常检出使用 `pnpm install --frozen-lockfile`。加依赖时进入具体的包执行 `pnpm add`，或使用 `pnpm --filter @ga/platform add`，并提交更新后的锁文件。

```bash
pnpm install --frozen-lockfile
pnpm dev                         # 平台端 http://localhost:5173，/api 反代到 GA_API_TARGET（默认 http://127.0.0.1:8080）
pnpm dev:agent                   # 代理商端 :5174（默认反代到 make run-agent 的 :8081）
pnpm dev:merchant                # 商户端 :5175（默认反代到 make run-merchant 的 :8082）
pnpm -r lint
pnpm -r typecheck
pnpm -r test
pnpm -r build
pnpm e2e                         # 需已运行测试后端；完整冒烟入口是仓库根目录的 make e2e
```

三个端使用独立后端和登录身份。主体端已有数据中心、员工账号、角色、日志、会话、个人中心和主账号专属 IP 黑白名单；代理商另有名下商户和邀请。先运行平台迁移、开通主体，再用主体编号登录。操作说明见[后台使用指南](../docs/guides/admin-guide.md)。

依赖方向：每个端的应用只能引用自己目录里的文件和 `@ga/shell` 的公开导出（包根、`@ga/shell/styles`、`@ga/shell/vite`、`@ga/shell/eslint`），不能引用别的端，由壳提供的 ESLint 规则 `ga/boundary` 检查。壳以源码形式被引用，不单独构建发布物。

用不到代理商端、商户端：删掉 `apps/agent`、`apps/merchant` 两个目录，跑一次 `pnpm install`（锁文件会去掉它们），别的不用改；后端不编译、不启动 `cmd/agent`、`cmd/merchant` 就行。

写页面看[新增模块指南](../docs/guides/new-module.md)的前端部分；约定看[开发约定](../docs/conventions.md)的“前端”一节；其他入口见[文档导航](../docs/README.md)。
