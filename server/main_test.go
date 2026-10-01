package main

// docs/api.md 与真实路由表的一致性检查（规范 §13.2 第 16 条的文档部分）：
//   - Public 路由的集合与文档"公开接口"一节完全一致；
//   - 每条端内路由的路径都在文档里出现过。

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

func TestAPIDocMatchesRoutes(t *testing.T) {
	gdb := db.OpenTestDB(t)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	p := cfg.Portals[conf.DefaultPortalCode]
	p.JWTSecret = "platform-test-secret-0123456789abcdef0123456789"
	cfg.Portals[conf.DefaultPortalCode] = p
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	a.Register(modules()...) // 与 main.go 注册的模块一致
	require.NoError(t, a.Migrate(context.Background()))
	require.NoError(t, a.Setup())

	doc, err := os.ReadFile(filepath.Join("..", "docs", "api.md"))
	require.NoError(t, err)
	text := string(doc)

	// "公开接口"一节里的表格行：| 方法 | `路径` | ...
	section := text[strings.Index(text, "## 公开接口"):]
	section = section[:strings.Index(section, "\n## ")]
	rowRe := regexp.MustCompile("(?m)^\\| *([A-Z]+) *\\| *`([^`]+)` *\\|")
	documented := map[string]bool{}
	for _, m := range rowRe.FindAllStringSubmatch(section, -1) {
		documented[m[1]+" "+m[2]] = true
	}
	require.NotEmpty(t, documented)

	actual := map[string]bool{}
	for _, r := range a.Routes() {
		rel := strings.TrimPrefix(r.Path, app.PortalPrefix(r.Portal))
		if r.Guard == rbac.GuardPublic {
			actual[r.Method+" "+rel] = true
		}
		// 路径在文档里出现过：写成完整相对路径，或者去掉第一段（模块前缀）后的路径
		short := rel
		if i := strings.Index(rel[1:], "/"); i >= 0 {
			short = rel[1+i:]
		}
		require.True(t, strings.Contains(text, "`"+rel+"`") || strings.Contains(text, "`"+short+"`"),
			"%s %s 没有写进 docs/api.md", r.Method, r.Path)
	}
	require.Equal(t, documented, actual, "docs/api.md 的公开接口清单与实际 Public 路由不一致")
}

// 后端镜像的构建上下文只 COPY 二进制需要的目录（deploy/Dockerfile.server 有意这么做，防止本地配置进镜像）：
// main.go 直接 import 的每个本仓库目录都必须在 COPY 清单里，否则镜像里 go build 会缺包（D-043）。
func TestDockerfileCopiesEveryDirectoryMainImports(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	dockerfile, err := os.ReadFile(filepath.Join("..", "deploy", "Dockerfile.server"))
	require.NoError(t, err)
	const module = "github.com/goalladmin/goalladmin/server/"
	need := map[string]bool{}
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"`+module) {
			continue
		}
		rest := strings.TrimPrefix(strings.Trim(line, `"`), module)
		top, _, _ := strings.Cut(rest, "/")
		need[top] = true
	}
	require.NotEmpty(t, need)
	for dir := range need {
		require.Contains(t, string(dockerfile), "COPY server/"+dir+" "+dir+"/", "Dockerfile.server 没有把 main.go 用到的 server/%s 复制进构建上下文", dir)
	}
}

// 内核的公开包（core/ 下、core/internal 之外）不能有带 TableName 的导出类型（规范 §16.2，D-043）：
// 业务模块能 import 这些类型，如果它同时是 gorm 模型，`db.From(ctx).Model(&rbac.Role{})` 就能绕过
// 内核服务直接读写内核的表（表名藏在方法里，源码里没有 ga_ 字面量）。
func TestCorePublicPackagesExposeNoGormModels(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir("core", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "internal" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != "TableName" || len(fd.Recv.List) == 0 {
				continue
			}
			recv := fd.Recv.List[0].Type
			if st, ok := recv.(*ast.StarExpr); ok {
				recv = st.X
			}
			if id, ok := recv.(*ast.Ident); ok && ast.IsExported(id.Name) {
				offenders = append(offenders, p+": "+id.Name)
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, offenders, "内核公开包里的导出类型不能带 TableName")
}
