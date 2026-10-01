# web

前端工作区。`packages/shell` 是框架壳 `@ga/shell`（业务方不改），`apps/<端>` 是各端应用。

pnpm 和 npm 都能用：根目录的脚本写的是 npm 的工作区语法，两个包管理器都认；`pnpm-lock.yaml` 是可复现安装的依据（CI、Docker 镜像用 `pnpm install --frozen-lockfile`），npm 生成的 `package-lock.json` 不提交。加依赖时用 pnpm，这样锁文件才会更新。

```bash
pnpm install                     # 或 npm install（npm 10 需要 .npmrc 里已经写好的 legacy-peer-deps）
npm run dev                      # 平台端 http://localhost:5173，/api 反代到 GA_API_TARGET（默认 http://127.0.0.1:8080）
npm run lint && npm run typecheck && npm test && npm run build
npm run e2e                      # 冒烟测试；一般用仓库根目录的 make e2e，它会先起后端
```

写页面看 `../docs/guides/new-module.md` 的前端部分；约定看 `../docs/conventions.md` 的"前端"一节。
