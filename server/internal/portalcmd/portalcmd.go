// Package portalcmd 是代理商、商户这类"只服务一个端"的程序的命令行（D-061）：cmd/agent、cmd/merchant 共用。
//
// 和平台程序（server/main.go）的区别：
//   - 只加载自己端的配置和密钥（conf.LoadFor），别的端的密钥不进这个进程；
//   - 不执行迁移：表结构只由平台程序迁移，这里启动时只读地核对，落后就拒绝启动（app.WithoutMigrations）；
//   - 没有 admin 命令：主体和主账号由平台端开；
//   - rbac prune 只清自己端的策略（别的端的权限码这个进程不认识）。
//
// 用法（以 merchant 为例）：
//
//	merchant                 [-config path]   启动 HTTP 服务（等于 serve）
//	merchant serve           [-config path]   启动 HTTP 服务
//	merchant migrate status  [-config path]   核对表结构是否已由平台程序迁移到最新
//	merchant rbac prune      [-config path]   清理本端策略里未注册的权限码
//	merchant healthcheck     [-addr host:port] 容器健康检查
//
// 配置路径优先级：-config 参数 > GA_CONFIG 环境变量 > config/<程序名>.yaml（存在时）> 仅默认值和环境变量。
package portalcmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/conf"
)

// kindCLI 是命令行操作的安全事件类型（与平台程序一致）。
const kindCLI = "cli"

// Program 描述一个只服务一个端的程序。
type Program struct {
	Name    string              // 程序名，也是默认配置文件名：config/<Name>.yaml
	Portal  string              // 只服务这一个端
	Modules func() []app.Module // 要注册的模块；只能是这个端的模块（依赖检查和路由表测试把关）
}

// Main 解析命令行并执行，返回进程退出码。
func (p Program) Main(args []string, stderr io.Writer) int {
	// 不带子命令、或者第一个参数是 -config 这样的选项时，当作 serve
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		args = append([]string{"serve"}, args...)
	}
	var err error
	switch args[0] {
	case "serve":
		err = p.cmdServe(args[1:])
	case "migrate":
		err = p.cmdMigrate(args[1:])
	case "rbac":
		err = p.cmdRBAC(args[1:])
	case "healthcheck":
		err = cmdHealthcheck(args[1:])
	case "help", "-h", "--help":
		p.usage(stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "未知命令 %q\n\n", args[0])
		p.usage(stderr)
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	return 0
}

func (p Program) usage(w io.Writer) {
	n := p.Name
	_, _ = fmt.Fprintf(w, `用法:
  %[1]s                  [-config path]     启动 HTTP 服务（不带子命令等于 serve）
  %[1]s serve            [-config path]     启动 HTTP 服务
  %[1]s migrate status   [-config path]     核对表结构是否已迁移到最新（迁移由平台程序执行：server migrate up）
  %[1]s rbac prune       [-config path]     清理本端策略里未注册的权限码
  %[1]s healthcheck      [-addr host:port]  容器健康检查
`, n)
}

// configFlag 解析 -config，并按优先级决定配置文件路径。
func (p Program) configFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("GA_CONFIG")
	if def == "" {
		if path := "config/" + p.Name + ".yaml"; fileExists(path) {
			def = path
		}
	}
	return fs.String("config", def, "配置文件路径（空表示只用默认值和环境变量）")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Build 构造 App：只加载本端的配置、不执行迁移、注册本端的模块。这是这个程序唯一的装配点。
func (p Program) Build(configPath string, opts ...app.Option) (*app.App, error) {
	cfg, err := conf.LoadFor(configPath, p.Portal)
	if err != nil {
		return nil, err
	}
	return p.BuildWith(cfg, opts...)
}

// BuildWith 用给定的配置构造 App（测试用）。
func (p Program) BuildWith(cfg *conf.Config, opts ...app.Option) (*app.App, error) {
	a, err := app.New(cfg, append([]app.Option{app.WithoutMigrations()}, opts...)...)
	if err != nil {
		return nil, err
	}
	if p.Modules != nil {
		a.Register(p.Modules()...)
	}
	return a, nil
}

func (p Program) cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := p.configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := p.Build(*cfgPath)
	if err != nil {
		return err
	}
	return a.Run(context.Background())
}

func (p Program) cmdMigrate(args []string) error {
	if len(args) < 1 || args[0] != "status" {
		return fmt.Errorf("%s 只有 migrate status：迁移由平台程序执行（server migrate up）", p.Name)
	}
	fs := flag.NewFlagSet("migrate status", flag.ExitOnError)
	cfgPath := p.configFlag(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	a, err := p.Build(*cfgPath)
	if err != nil {
		return err
	}
	ctx := a.Context(context.Background())
	defer func() { _ = a.Stop(ctx) }()
	if err := a.CheckMigrations(ctx); err != nil {
		return err
	}
	fmt.Println("表结构已是最新")
	return nil
}

func (p Program) cmdRBAC(args []string) error {
	if len(args) < 1 || args[0] != "prune" {
		return errors.New("rbac 需要子命令: prune")
	}
	fs := flag.NewFlagSet("rbac prune", flag.ExitOnError)
	cfgPath := p.configFlag(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	a, err := p.Build(*cfgPath)
	if err != nil {
		return err
	}
	ctx := a.Context(context.Background())
	defer func() { _ = a.Stop(ctx) }()
	if err := a.CheckMigrations(ctx); err != nil {
		return err
	}
	if err := a.Setup(); err != nil {
		return err
	}
	n, err := a.Deps().RBAC.Prune(ctx)
	if err != nil {
		return err
	}
	// 命令行改授权数据：记一个安全事件（D-032）
	if err := a.Deps().Audit.RecordSecurity(ctx, audit.NewSecurityEvent{Portal: p.Portal, Kind: kindCLI, Detail: fmt.Sprintf("rbac prune: %d", n)}); err != nil {
		return err
	}
	fmt.Printf("已删除 %d 条未注册权限码的策略（只限 %s 端）\n", n, p.Portal)
	return nil
}

func cmdHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "服务地址")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+*addr+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz 返回 %d", resp.StatusCode)
	}
	return nil
}

// CheckRoutes 核对 App 注册的路由只属于本程序的端（D-061）：端内路由都在 /api/<端>/v1 下，Raw 路由只有健康检查。
// 每个程序的测试调用它，模块装错了（把别的端的路由带进来）CI 就失败。必须在 Setup 之后调用。
func (p Program) CheckRoutes(a *app.App) error {
	prefix := app.PortalPrefix(p.Portal) + "/"
	var bad []string
	for _, r := range a.Routes() {
		if r.Portal != p.Portal || !strings.HasPrefix(r.Path, prefix) {
			bad = append(bad, fmt.Sprintf("%s %s（端 %s）", r.Method, r.Path, r.Portal))
		}
	}
	for _, r := range a.RawRoutes() {
		if r.Path != "/healthz" && r.Path != "/readyz" {
			bad = append(bad, fmt.Sprintf("%s %s（Raw：%s）", r.Method, r.Path, r.Purpose))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s 程序注册了不属于 %s 端的路由：\n  %s", p.Name, p.Portal, strings.Join(bad, "\n  "))
	}
	return nil
}
