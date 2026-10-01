// Package db 负责数据库句柄的获取与事务边界。
//
// 没有包级全局变量：基础句柄由 app 放进 ctx（WithDB），业务代码一律 From(ctx)。
// 事务用 Tx(ctx, fn)；fn 收到的 ctx 里带着事务句柄，嵌套调用自动加入同一事务。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/utils"

	"github.com/goalladmin/goalladmin/server/core/conf"
)

type (
	baseKey  struct{}
	txKey    struct{}
	hooksKey struct{}
)

// txHooks 收集事务提交后要执行的函数；同一事务内的嵌套调用共享同一个实例。
type txHooks struct {
	fns  []func()               // 提交之后
	ends []func(committed bool) // 事务结束之后，不论提交还是回滚（包括 fn 里 panic）
}

// ErrNoDB 表示 ctx 里没有数据库句柄。这是编程错误：ctx 必须来自 app（请求、Module.Start）
// 或由调用方 WithDB 显式放入。
var ErrNoDB = errors.New("db: ctx 里没有数据库句柄（ctx 不是由 app 创建的？）")

// Open 按配置打开 MySQL 连接池，确认数据库可用后返回。
//
// cfg.ConnectWait 为 0 时只试一次，连不上立即返回错误；大于 0 时每 retryInterval 重试一次，
// 直到连上或等满（D-037）。连接串里带连接和读写超时（conf.Database.DSN）。
func Open(cfg conf.Database, log *slog.Logger) (*gorm.DB, error) {
	if log == nil {
		log = slog.Default()
	}
	dsn := cfg.DSN()
	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	ping := func(ctx context.Context) error { return sqlDB.PingContext(ctx) }
	if err := waitReady(context.Background(), ping, cfg.ConnectWait, attemptTimeout(cfg), retryInterval, log); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	gdb, err := openGorm(sqlDB, dsn, log, cfg.SlowThreshold)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return gdb, nil
}

// OpenMigrator 为执行迁移单独打开一个只有一条连接的连接池（D-037）：连接串不带框架默认的读写超时，
// 大表上的 ALTER TABLE 不会被单次读超时掐断。用完调 Close。调用前主连接池已经连上，所以这里不等待。
func OpenMigrator(cfg conf.Database, log *slog.Logger) (*gorm.DB, error) {
	if log == nil {
		log = slog.Default()
	}
	dsn := cfg.MigrationDSN()
	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout(cfg))
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	gdb, err := openGorm(sqlDB, dsn, log, cfg.SlowThreshold)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return gdb, nil
}

// openGorm 在已经连上的连接池上建 gorm 句柄。DSN 只用来解析时区等设置（SQL 日志里的时间按 loc 显示），不会再开连接。
func openGorm(sqlDB *sql.DB, dsn string, log *slog.Logger, slow time.Duration) (*gorm.DB, error) {
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, DSN: dsn}), &gorm.Config{
		Logger:                                   newSlogLogger(log, slow),
		DisableForeignKeyConstraintWhenMigrating: true,
		NowFunc:                                  func() time.Time { return time.Now().UTC() },
		TranslateError:                           true,
	})
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	return gdb, nil
}

// retryInterval 是启动等待数据库时两次尝试的间隔。
const retryInterval = 2 * time.Second

// attemptTimeout 是单次 Ping 的上限：连接超时再留一点余量给握手后的往返。
func attemptTimeout(cfg conf.Database) time.Duration {
	if cfg.ConnectTimeout <= 0 {
		return 5 * time.Second
	}
	return cfg.ConnectTimeout + time.Second
}

// waitReady 反复调用 ping，直到成功或等满 wait（0 表示只试一次）。每次失败打一行警告。
func waitReady(ctx context.Context, ping func(context.Context) error, wait, attempt, interval time.Duration, log *slog.Logger) error {
	start := time.Now()
	for n := 1; ; n++ {
		actx, cancel := context.WithTimeout(ctx, attempt)
		err := ping(actx)
		cancel()
		if err == nil {
			if n > 1 {
				log.Info("database ready", "attempts", n, "waited", time.Since(start).Round(time.Millisecond).String())
			}
			return nil
		}
		waited := time.Since(start)
		if wait <= 0 {
			return fmt.Errorf("db: ping: %w（数据库和服务一起启动时，可以用 database.connectWait / GA_DB_CONNECT_WAIT 让服务等它就绪）", err)
		}
		remaining := wait - waited
		if remaining <= 0 {
			return fmt.Errorf("db: 等了 %s 数据库仍不可用: %w", waited.Round(time.Second), err)
		}
		log.Warn("database not ready, retrying", "attempt", n, "waited", waited.Round(time.Second).String(), "wait", wait.String(), "err", err)
		// 最后一次正好在等满时再试一次
		select {
		case <-ctx.Done():
			return fmt.Errorf("db: ping: %w", ctx.Err())
		case <-time.After(min(interval, remaining)):
		}
	}
}

// OpenDSN 用连接串打开数据库，主要供测试和命令行工具使用。
func OpenDSN(dsn string, log *slog.Logger) (*gorm.DB, error) {
	if log == nil {
		log = slog.Default()
	}
	gdb, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:         newSlogLogger(log, 200*time.Millisecond),
		NowFunc:        func() time.Time { return time.Now().UTC() },
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	return gdb, nil
}

// WithDB 把基础句柄放进 ctx。app 会为每个请求和 Module.Start 调用它；CLI 和测试自己调用。
func WithDB(ctx context.Context, gdb *gorm.DB) context.Context {
	return context.WithValue(ctx, baseKey{}, gdb)
}

// From 返回当前应使用的句柄：ctx 里有事务就返回事务，否则返回基础句柄（已绑定 ctx）。
// ctx 里两者都没有时 panic（ErrNoDB），因为这是编程错误而不是运行时故障。
func From(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx
	}
	if base, ok := ctx.Value(baseKey{}).(*gorm.DB); ok && base != nil {
		return base.WithContext(ctx)
	}
	panic(ErrNoDB)
}

// Has 报告 ctx 里是否带有数据库句柄（事务或基础句柄）。From 在没有句柄时会 panic，需要静默降级的地方先用它判断。
func Has(ctx context.Context) bool {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return true
	}
	base, ok := ctx.Value(baseKey{}).(*gorm.DB)
	return ok && base != nil
}

// InTx 报告 ctx 是否已在事务里。
func InTx(ctx context.Context) bool {
	tx, ok := ctx.Value(txKey{}).(*gorm.DB)
	return ok && tx != nil
}

// Tx 在事务里执行 fn。fn 返回错误或 panic 时回滚，否则提交。
// 如果 ctx 已在事务里，fn 直接加入该事务（不开新事务，不单独提交）。
func Tx(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}
	base, ok := ctx.Value(baseKey{}).(*gorm.DB)
	if !ok || base == nil {
		return ErrNoDB
	}
	hooks := &txHooks{}
	committed := false
	defer func() {
		for _, f := range hooks.ends {
			f(committed)
		}
	}()
	err := base.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ctx := context.WithValue(ctx, txKey{}, tx)
		ctx = context.WithValue(ctx, hooksKey{}, hooks)
		return fn(ctx)
	})
	if err != nil {
		return err
	}
	committed = true
	for _, f := range hooks.fns {
		f()
	}
	return nil
}

// AfterEnd 让 fn 在当前事务结束之后执行，不论提交还是回滚（fn 里 panic 导致的回滚也算），committed 说明结果；
// 在 AfterCommit 的回调之后执行。不在可写事务里时立即执行、committed 为 true（写入已经自动提交）。
// 用于"事务里设了一个标记、事务结束必须撤掉"的场合（D-055）。
func AfterEnd(ctx context.Context, fn func(committed bool)) {
	if h, ok := ctx.Value(hooksKey{}).(*txHooks); ok && h != nil && InTx(ctx) {
		h.ends = append(h.ends, fn)
		return
	}
	fn(true)
}

// Snapshot 在只读、可重复读的事务里执行 fn：fn 里的多条查询看到的是同一时刻已提交的数据，
// 中途别人提交的写入一条也看不到（D-051）。用在"检查之后还要再读"或"几张表要成套读"的地方。
// ctx 已在事务里时直接用那个事务（看到的是那个事务的视图）。fn 里不能写库。
func Snapshot(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}
	base, ok := ctx.Value(baseKey{}).(*gorm.DB)
	if !ok || base == nil {
		return ErrNoDB
	}
	return base.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

// AfterCommit 让 fn 在当前事务提交成功之后执行；不在事务里时立即执行。
//
// 清缓存这类副作用应放在这里而不是直接写在事务体内：写在事务体内的话，回滚后缓存已经被清（无害但多余），
// 更要紧的是提交前并发请求可能用旧数据把缓存填回去，让"吊销、停用立即生效"落空。
func AfterCommit(ctx context.Context, fn func()) {
	if h, ok := ctx.Value(hooksKey{}).(*txHooks); ok && h != nil && InTx(ctx) {
		h.fns = append(h.fns, fn)
		return
	}
	fn()
}

// Ping 检查连接是否可用，供 /readyz 使用。
func Ping(ctx context.Context, gdb *gorm.DB) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Close 关闭连接池。
func Close(gdb *gorm.DB) error {
	if gdb == nil {
		return nil
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// ---- GORM logger → slog 适配 ----

type slogLogger struct {
	log           *slog.Logger
	slowThreshold time.Duration
	level         gormlogger.LogLevel
}

func newSlogLogger(log *slog.Logger, slow time.Duration) gormlogger.Interface {
	if slow <= 0 {
		slow = 200 * time.Millisecond
	}
	return &slogLogger{log: log.With("component", "gorm"), slowThreshold: slow, level: gormlogger.Warn}
}

func (l *slogLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	c := *l
	c.level = level
	return &c
}

func (l *slogLogger) Info(ctx context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Info {
		l.log.InfoContext(ctx, fmt.Sprintf(msg, args...))
	}
}

func (l *slogLogger) Warn(ctx context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Warn {
		l.log.WarnContext(ctx, fmt.Sprintf(msg, args...))
	}
}

func (l *slogLogger) Error(ctx context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Error {
		l.log.ErrorContext(ctx, fmt.Sprintf(msg, args...))
	}
}

// SQL 日志只记占位符模板，不记绑定参数的值（D-054）：出错和慢查询的日志会写进应用输出和集中日志，参数里有
// 密码哈希、刷新凭证的哈希、联系方式这类不该进日志的东西（规范 §5.6）。
//
// gorm 看到 logger 实现了 ParamsFilter，就用它的返回值拼日志里的 SQL。但 Scan 这条路径经过 gorm 自己的记录器，
// 参数已经被展开、不经过这里。所以这里给模板打一个标记，Trace 只记带标记的 SQL；不带标记的（参数已被展开）不记 SQL 文本，
// 只记调用位置。
const sqlTemplateMark = "\x00ga-sql-template\x00"

// ParamsFilter 实现 gorm.ParamsFilter：返回带标记的模板、丢掉参数。
func (l *slogLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sqlTemplateMark + sql, nil
}

// sqlForLog 返回可以写进日志的 SQL：带标记的是模板，照写；不带标记的参数已被展开，不写。
func sqlForLog(s string) string {
	if t, ok := strings.CutPrefix(s, sqlTemplateMark); ok {
		return t
	}
	return "[omitted: parameters were expanded]"
}

func (l *slogLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && l.level >= gormlogger.Error:
		sql, rows := fc()
		l.log.ErrorContext(ctx, "sql error", "err", err, "elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sqlForLog(sql), "src", utils.FileWithLineNum())
	case elapsed > l.slowThreshold && l.level >= gormlogger.Warn:
		sql, rows := fc()
		l.log.WarnContext(ctx, "slow sql", "elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sqlForLog(sql), "src", utils.FileWithLineNum())
	case l.level >= gormlogger.Info:
		sql, rows := fc()
		l.log.DebugContext(ctx, "sql", "elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sqlForLog(sql), "src", utils.FileWithLineNum())
	}
}
