package app

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/internal/metrics"
	"github.com/goalladmin/goalladmin/server/core/monitor"
)

// 健康检查的路径：不计入请求统计（D-030）。
const (
	healthPath = "/healthz"
	readyPath  = "/readyz"
)

// monitorService 把进程内的指标采集器和数据库连接池交给模块（monitor.Service）。
// 配置关掉服务器状态时只回 Enabled=false；否则结果在进程内缓存 ttl（D-031），
// 同时打开页面的人再多，每个周期也只读一次内存统计、测一次数据库。
type monitorService struct {
	c       *metrics.Collector
	db      *gorm.DB
	enabled bool
	ttl     time.Duration
	now     func() time.Time

	mu   sync.Mutex
	last monitor.Server
	at   time.Time
}

func (s *monitorService) Server(ctx context.Context) (monitor.Server, error) {
	if !s.enabled {
		return monitor.Server{Enabled: false}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.ttl > 0 && !s.at.IsZero() && now.Sub(s.at) < s.ttl {
		return s.last, nil
	}
	var sqlDB *sql.DB
	if s.db != nil {
		if d, err := s.db.DB(); err == nil {
			sqlDB = d
		}
	}
	s.last, s.at = s.c.Server(ctx, sqlDB), now
	return s.last, nil
}
