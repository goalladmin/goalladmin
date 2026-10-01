// 单飞刷新（规范 §5.5）：同一时刻只允许一个刷新请求在途，其他调用方共享它的结果。

export class RefreshGate<T> {
  private inflight: Promise<T> | null = null
  private calls = 0

  constructor(private readonly doRefresh: () => Promise<T>) {}

  /** 返回在途的刷新；没有在途的就发起一个。 */
  refresh(): Promise<T> {
    if (!this.inflight) {
      this.calls++
      this.inflight = this.doRefresh().finally(() => {
        this.inflight = null
      })
    }
    return this.inflight
  }

  /** 已发起过的真实刷新次数（测试用）。 */
  get count(): number {
    return this.calls
  }

  get pending(): boolean {
    return this.inflight !== null
  }
}
