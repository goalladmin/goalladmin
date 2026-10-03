package app

// 167：用独立测试子进程隔离内存、连接池和订阅；控制通道只存在于测试管道。
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

type processInput struct {
	DSN, Redis, Prefix string
	Now                time.Time
	Op, ID             string
}

type processReply struct {
	URL, Source, Answer string
	PID                 int
}

// 子进程只注册测试端；父进程准备临时库，子进程不创建或删除库。
func TestMultiProcess_167_Child(t *testing.T) {
	if os.Getenv("GA_TEST_PROCESS_CHILD") != "1" {
		t.Skip("仅供父测试通过管道启动")
	}
	output := os.NewFile(3, "test-control")
	require.NotNil(t, output)
	defer func() { _ = output.Close() }()
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(output)
	var input processInput
	require.NoError(t, dec.Decode(&input))
	gdb, err := db.OpenDSN(input.DSN, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(gdb)) })
	clock := &fakeClock{t: input.Now}
	users := newMemUsers()
	module := &sharedCountsModule{
		invalidationTestModule: invalidationTestModule{authTestModule{users: users}},
		policy: portal.LoginPolicy{CaptchaAlways: true, LockAfterFailures: 4, AccountLockAfter: 8,
			IPRatePerMinute: 120, AccountRatePerMinute: 120},
	}
	a := newInvalidationApp(t, gdb, clock, input.Redis, input.Prefix, testPortal, testSecret, module)
	f := &authFixture{t: t, app: a, users: users, clock: clock}
	f.addUser("alice", "alice-pass-123")
	f.addUser("bob", "bob-pass-123")
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)
	require.NoError(t, enc.Encode(processReply{URL: server.URL, Source: a.invalidationSource, PID: os.Getpid()}))
	ctx := a.Context(context.Background())
	cli := auth.Principal{Portal: testPortal, Super: true}
	var roleID uint64
	for {
		if err := dec.Decode(&input); err == io.EOF {
			return
		} else {
			require.NoError(t, err)
		}
		reply := processReply{}
		switch input.Op {
		case "peek":
			reply.Answer = a.captcha.Peek(input.ID)
		case "grant":
			role, err := a.Deps().RBAC.CreateRole(ctx, cli, testPortal, rbac.RoleInput{Code: "reader", Name: "Reader", Status: 1})
			require.NoError(t, err)
			roleID = role.ID
			require.NoError(t, a.Deps().RBAC.GrantRolePerms(ctx, cli, testPortal, roleID, []string{invalidationPerm}))
			require.NoError(t, a.Deps().RBAC.AssignUserRoles(ctx, cli, testPortal, 1, []uint64{roleID}))
		case "revoke":
			require.NotZero(t, roleID)
			require.NoError(t, a.Deps().RBAC.GrantRolePerms(ctx, cli, testPortal, roleID, nil))
		default:
			t.Fatalf("未知测试命令 %q", input.Op)
		}
		require.NoError(t, enc.Encode(reply))
	}
}

type testProcess struct {
	t       *testing.T
	cmd     *exec.Cmd
	input   io.WriteCloser
	replies <-chan processReply
	done    <-chan error
	url     string
	source  string
	pid     int
	logPath string
	stopped bool
}

func startTestProcess(t *testing.T, input processInput) *testProcess {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestMultiProcess_167_Child$", "-test.timeout=80s")
	cmd.Env = append(os.Environ(), "GA_TEST_PROCESS_CHILD=1")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	t.Cleanup(func() { _ = writer.Close() })
	logPath := filepath.Join(t.TempDir(), "process.log")
	logFile, err := os.Create(logPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = logFile.Close() })
	cmd.Stdout, cmd.Stderr, cmd.ExtraFiles = logFile, logFile, []*os.File{writer}
	require.NoError(t, cmd.Start())
	require.NoError(t, writer.Close())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	replies := make(chan processReply, 1)
	go func() {
		defer close(replies)
		decoder := json.NewDecoder(reader)
		for {
			var reply processReply
			if decoder.Decode(&reply) != nil {
				return
			}
			select {
			case replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	p := &testProcess{t: t, cmd: cmd, input: stdin, replies: replies, done: done, logPath: logPath}
	t.Cleanup(p.stop)
	require.NoError(t, json.NewEncoder(stdin).Encode(input))
	ready := p.receive()
	p.url, p.source, p.pid = ready.URL, ready.Source, ready.PID
	require.NotEmpty(t, p.url)
	require.NotEmpty(t, p.source)
	return p
}

func (p *testProcess) receive() processReply {
	p.t.Helper()
	select {
	case reply, ok := <-p.replies:
		if ok {
			return reply
		}
	case <-time.After(20 * time.Second):
	}
	logs, _ := os.ReadFile(p.logPath)
	p.t.Fatalf("子进程控制通道失败: %s", logs)
	return processReply{}
}

func (p *testProcess) control(op, id string) processReply {
	p.t.Helper()
	require.NoError(p.t, json.NewEncoder(p.input).Encode(processInput{Op: op, ID: id}))
	return p.receive()
}

func (p *testProcess) stop() {
	p.t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	_ = p.input.Close()
	select {
	case err := <-p.done:
		logs, _ := os.ReadFile(p.logPath)
		require.NoError(p.t, err, "子进程日志: %s", logs)
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		p.t.Error("子进程未在时限内退出")
	}
}

func (p *testProcess) request(method, path string, body any, token string) httpx.Envelope {
	p.t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(p.t, err)
		payload = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequest(method, p.url+"/api/"+testPortal+"/v1"+path, payload)
	require.NoError(p.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("X-GA-Client", "web")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	require.NoError(p.t, err)
	defer func() { _ = res.Body.Close() }()
	var envelope httpx.Envelope
	require.NoError(p.t, json.NewDecoder(res.Body).Decode(&envelope))
	return envelope
}

func TestMultiProcess_167_SharedState(t *testing.T) {
	gdb, clock, redisAddr, prefix := newInvalidationDB(t)
	var database string
	require.NoError(t, gdb.Raw("SELECT DATABASE()").Scan(&database).Error)
	dsn, err := mysqldriver.ParseDSN(os.Getenv(db.TestDSNEnv))
	require.NoError(t, err)
	dsn.DBName = database
	input := processInput{DSN: dsn.FormatDSN(), Redis: redisAddr, Prefix: prefix, Now: clock.Now()}
	a, b := startTestProcess(t, input), startTestProcess(t, input)
	require.NotEqual(t, os.Getpid(), a.pid)
	require.NotEqual(t, a.pid, b.pid)
	require.NotEqual(t, a.source, b.source)
	b.control("grant", "")
	challenge := func(from *testProcess, username, password string) gin.H {
		cap := from.request("GET", "/auth/captcha", nil, "")
		require.Zero(t, cap.Code)
		data := cap.Data.(map[string]any)
		require.Len(t, data, 2)
		id := data["captchaId"].(string)
		require.True(t, strings.HasPrefix(id, "r"), "必须通过 Redis 共享")
		answer := from.control("peek", id).Answer
		require.Len(t, answer, 5)
		return gin.H{"username": username, "password": password, "captchaId": id, "captchaCode": answer}
	}
	var access string
	for _, pair := range [][2]*testProcess{{a, b}, {b, a}} {
		body := challenge(pair[0], "alice", "alice-pass-123")
		login := pair[1].request("POST", "/auth/login", body, "")
		require.Zero(t, login.Code)
		access = login.Data.(map[string]any)["accessToken"].(string)
		require.NotEmpty(t, access)
		require.Equal(t, httpx.CodeCaptchaRequired, pair[0].request("POST", "/auth/login", body, "").Code)
	}
	// 两边先缓存授权；时钟冻结，撤权传播不能靠 15 秒 TTL。
	for _, p := range []*testProcess{a, b} {
		require.Zero(t, p.request("GET", "/protected", nil, access).Code)
	}
	b.control("revoke", "")
	require.Eventually(t, func() bool {
		return a.request("GET", "/protected", nil, access).Code == httpx.CodeForbidden
	}, 2*time.Second, 10*time.Millisecond)
	// 分散在两个进程的失败必须合计；各自只有两次失败，不足以触发四次阈值。
	for i := range 4 {
		from, to := a, b
		if i%2 != 0 {
			from, to = b, a
		}
		require.Equal(t, httpx.CodeLoginFailed, to.request("POST", "/auth/login", challenge(from, "bob", "wrong-pass-123"), "").Code)
	}
	for _, p := range []*testProcess{a, b} {
		locked := p.request("POST", "/auth/login", challenge(p, "bob", "bob-pass-123"), "")
		require.Equal(t, httpx.CodeLocked, locked.Code)
	}
	// 一边退出，另一边仍可验证已有会话并处理新登录。
	a.stop()
	require.Zero(t, b.request("GET", "/ping", nil, access).Code)
	require.Zero(t, b.request("POST", "/auth/login", challenge(b, "alice", "alice-pass-123"), "").Code)
}
