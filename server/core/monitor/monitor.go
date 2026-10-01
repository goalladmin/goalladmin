// Package monitor 是服务器状态的公开入口（D-030）：进程自身的只读指标。
//
// 指标由框架在进程内采集：每个请求按分钟、按路由模板计数，保留最近 60 分钟；CPU、内存、协程数、GC 和
// 数据库连接池在读取时现取。没有告警、没有历史存储、不汇总多个实例（v0.1 单实例，规范 §1.3）。
// 模块通过 app.Deps.Monitor 读取；框架的健康检查接口不计入请求统计。
package monitor

import (
	"context"
	"time"
)

// Service 读取服务器状态。
type Service interface {
	// Server 返回当前进程的状态和最近 60 分钟的请求统计。
	Server(ctx context.Context) (Server, error)
}

// Server 是一次读取的结果。配置关掉服务器状态（monitor.server: false，D-031）时 Enabled 为 false，其余字段为空。
type Server struct {
	Enabled   bool      `json:"enabled"`
	Now       time.Time `json:"now"`
	StartedAt time.Time `json:"startedAt"`
	Uptime    int64     `json:"uptime"` // 秒
	Runtime   Runtime   `json:"runtime"`
	CPU       CPU       `json:"cpu"`
	Memory    Memory    `json:"memory"`
	DB        DB        `json:"db"`
	Requests  Requests  `json:"requests"`
}

// Runtime 是运行环境。
type Runtime struct {
	GoVersion  string `json:"goVersion"` // 只到大版本，如 go1.26（D-031）
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Version    string `json:"version"` // 构建信息里的主模块版本；本地构建为 (devel)
	NumCPU     int    `json:"numCpu"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	Goroutines int    `json:"goroutines"`
}

// CPU 是进程的 CPU 占用。
type CPU struct {
	// Supported 为 false 时本系统取不到进程 CPU 时间，Percent 无意义。
	Supported bool `json:"supported"`
	// Percent 是最近一段时间进程占全部 CPU 核心的百分比（0–100）。
	Percent float64 `json:"percent"`
}

// Memory 是 Go 运行时的内存情况（字节）。
type Memory struct {
	Sys         uint64     `json:"sys"`       // 向操作系统申请的总量
	HeapAlloc   uint64     `json:"heapAlloc"` // 堆上存活对象
	HeapInuse   uint64     `json:"heapInuse"`
	StackInuse  uint64     `json:"stackInuse"`
	HeapObjects uint64     `json:"heapObjects"`
	NumGC       uint32     `json:"numGc"`
	PauseTotal  float64    `json:"pauseTotalMs"`
	LastGC      *time.Time `json:"lastGc"`
}

// DB 是数据库连接池和连通情况。
type DB struct {
	OK        bool    `json:"ok"`
	LatencyMs float64 `json:"latencyMs"` // 一次 ping 的耗时
	Version   string  `json:"version"`   // 只到大版本，如 8.0、10.11 MariaDB（D-031）
	MaxOpen   int     `json:"maxOpen"`   // 0 表示不限
	Open      int     `json:"open"`
	InUse     int     `json:"inUse"`
	Idle      int     `json:"idle"`
	WaitCount int64   `json:"waitCount"`
	WaitMs    float64 `json:"waitMs"`
}

// Requests 是最近 60 分钟的请求统计。
type Requests struct {
	// Minutes 按时间先后排列，最后一项是当前这一分钟（还没结束）。
	Minutes []Minute `json:"minutes"`
	// Routes 是这 60 分钟里请求最多的路由（按路由模板，不按原始路径）。
	Routes []Route `json:"routes"`
}

// Minute 是一分钟内的统计。没有请求的分钟各项为 0，资源类指标为 nil。
type Minute struct {
	At           time.Time `json:"at"`
	Count        int64     `json:"count"`
	ClientErrors int64     `json:"clientErrors"` // 4xx
	ServerErrors int64     `json:"serverErrors"` // 5xx
	AvgMs        float64   `json:"avgMs"`
	P95Ms        float64   `json:"p95Ms"` // 按分桶估算
	MaxMs        float64   `json:"maxMs"`
	CPU          *float64  `json:"cpu"`        // 这一分钟里进程的 CPU 占用（%）
	HeapInuse    *uint64   `json:"heapInuse"`  // 这一分钟开始时的堆占用
	Goroutines   *int      `json:"goroutines"` // 这一分钟开始时的协程数
}

// Route 是一个路由在统计窗口内的汇总。
type Route struct {
	Method       string  `json:"method"`
	Route        string  `json:"route"` // 路由模板，例如 /api/platform/v1/system/users/:id；没匹配到的请求记为空
	Count        int64   `json:"count"`
	ServerErrors int64   `json:"serverErrors"`
	AvgMs        float64 `json:"avgMs"`
	P95Ms        float64 `json:"p95Ms"`
}
