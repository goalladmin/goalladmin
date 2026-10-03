import { fileURLToPath } from 'node:url'

import { defineConfig, devices } from '@playwright/test'

// 冒烟测试（规范 §13.3）：真实后端 + vite preview 托管的构建产物。三个端各起一个预览服务器：平台端（4173）、
// 代理商端（4175）、商户端（4176），分别反代到 scripts/e2e.sh 起好的三个后端程序（D-061、D-067）。
// 一般通过仓库根目录的 scripts/e2e.sh 运行：它负责起后端、建管理员并把账号密码放进环境变量。
const port = Number(process.env.GA_E2E_WEB_PORT ?? 4173)
const agentPort = Number(process.env.GA_E2E_AGENT_WEB_PORT ?? 4175)
const merchantPort = Number(process.env.GA_E2E_MERCHANT_WEB_PORT ?? 4176)
// 没法下载 Playwright 自带浏览器的环境，可以用 GA_E2E_CHROME 指定一个 Chromium 可执行文件。
const executablePath = process.env.GA_E2E_CHROME

export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    locale: 'zh-CN',
    testIdAttribute: 'data-test',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions: executablePath ? { executablePath } : {},
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: [
    {
      command: `npm run build && npm run preview -- --port ${port} --strictPort --host 127.0.0.1`,
      url: `http://127.0.0.1:${port}/login`,
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
    },
    ...[
      { app: 'agent', port: agentPort, api: process.env.GA_E2E_AGENT_API ?? 'http://127.0.0.1:18081' },
      { app: 'merchant', port: merchantPort, api: process.env.GA_E2E_MERCHANT_API ?? 'http://127.0.0.1:18082' },
    ].map((s) => ({
      command: `npm run build && npm run preview -- --port ${s.port} --strictPort --host 127.0.0.1`,
      cwd: fileURLToPath(new URL(`../${s.app}/`, import.meta.url)),
      url: `http://127.0.0.1:${s.port}/login`,
      env: { GA_API_TARGET: s.api },
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
    })),
  ],
})
