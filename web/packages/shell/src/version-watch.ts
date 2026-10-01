// 版本提示：前端定期读部署目录里的 version.json（构建时生成），编号和自己不一样，
// 说明服务器上已经换了新版本（重新部署、升级了框架），提示"系统已更新，请刷新"。
// 只提示一次；标签页回到前台时立即检查一次。开发服务器上没有编号，不检查。

export interface VersionWatchOptions {
  /** 当前页面的构建编号；为空时不检查。 */
  current: string | undefined
  /** version.json 的地址。 */
  url: string
  /** 发现新版本时调用一次。 */
  onUpdate: () => void
  /** 检查间隔，默认 5 分钟。 */
  intervalMs?: number
  fetcher?: (url: string) => Promise<{ build?: unknown } | null>
}

const defaultFetcher = async (url: string) => {
  const res = await fetch(`${url}?t=${Date.now()}`, { cache: 'no-store' })
  if (!res.ok) return null
  return (await res.json()) as { build?: unknown }
}

/** 开始检查，返回停止函数。 */
export function watchVersion(o: VersionWatchOptions): () => void {
  if (!o.current || typeof window === 'undefined') return () => {}
  const fetcher = o.fetcher ?? defaultFetcher
  let done = false
  const check = async () => {
    if (done) return
    try {
      const v = await fetcher(o.url)
      if (!done && v && typeof v.build === 'string' && v.build !== '' && v.build !== o.current) {
        done = true
        o.onUpdate()
      }
    } catch {
      // 网络不通、文件不存在：下一轮再试
    }
  }
  const timer = window.setInterval(() => void check(), o.intervalMs ?? 5 * 60 * 1000)
  const onVisible = () => {
    if (document.visibilityState === 'visible') void check()
  }
  document.addEventListener('visibilitychange', onVisible)
  return () => {
    window.clearInterval(timer)
    document.removeEventListener('visibilitychange', onVisible)
  }
}
