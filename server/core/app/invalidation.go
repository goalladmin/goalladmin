package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/goalladmin/goalladmin/server/core/portal"
)

// 失效消息只描述缓存键，不承载状态；状态始终从数据库读取（D-075）。
type invalidation struct {
	Version int    `json:"version"`
	Source  string `json:"source"`
	Kind    string `json:"kind"`
	Portal  string `json:"portal,omitempty"`
	Key     string `json:"key,omitempty"`
}

func newInvalidationSource() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func validHexID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (e invalidation) valid() bool {
	if e.Version != 1 || !validHexID(e.Source) {
		return false
	}
	switch e.Kind {
	case "session":
		return portal.ValidCode(e.Portal) && (e.Key == "" || validHexID(e.Key))
	case "account":
		if !portal.ValidCode(e.Portal) {
			return false
		}
		if e.Key == "" {
			return true
		}
		id, err := strconv.ParseUint(e.Key, 10, 64)
		return err == nil && id != 0 && strconv.FormatUint(id, 10) == e.Key
	case "org", "menu":
		return portal.ValidCode(e.Portal) && e.Key == ""
	case "policy", "ip":
		return e.Portal == "" && e.Key == ""
	case "dict":
		if e.Portal != "" || len(e.Key) > 64 {
			return false
		}
		for _, c := range e.Key {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (a *App) publishInvalidation(kind, portal, key string) {
	if a.redis == nil {
		return
	}
	e := invalidation{Version: 1, Source: a.invalidationSource, Kind: kind, Portal: portal, Key: key}
	if !e.valid() {
		return
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return
	}
	// 提交后的副作用不随原请求取消；Do 自带 500 毫秒上限，不重试。
	if err := a.redis.Publish(context.Background(), string(payload)); err != nil {
		a.deps.Log.Debug("cache invalidation publish failed", "kind", kind, "err", err)
	}
}

func (a *App) publishAccount(portal string, userID uint64) {
	key := ""
	if userID != 0 {
		key = strconv.FormatUint(userID, 10)
	}
	a.publishInvalidation("account", portal, key)
}

func (a *App) accountChanged(portal string, userID uint64) {
	if an := a.authenticators[portal]; an != nil {
		an.InvalidateAccount(userID)
	}
	a.publishAccount(portal, userID)
}

func (a *App) sessionChanged(portal, sid string) {
	if an := a.authenticators[portal]; an != nil {
		an.InvalidateSession(sid)
	}
	a.publishInvalidation("session", portal, sid)
}

func (a *App) orgChanged(portal string) {
	if an := a.authenticators[portal]; an != nil {
		an.InvalidateAccount(0)
	}
	a.publishInvalidation("org", portal, "")
}

func (a *App) receiveInvalidation(payload string) {
	// 探测的空消息、超长消息和未知协议都不触发缓存失效。
	if len(payload) == 0 || len(payload) > 1024 {
		return
	}
	var e invalidation
	d := json.NewDecoder(strings.NewReader(payload))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF || !e.valid() || e.Source == a.invalidationSource {
		return
	}
	switch e.Kind {
	case "session":
		if an := a.authenticators[e.Portal]; an != nil {
			an.InvalidateSession(e.Key)
		}
	case "account", "org":
		if an := a.authenticators[e.Portal]; an != nil {
			var id uint64
			if e.Key != "" {
				id, _ = strconv.ParseUint(e.Key, 10, 64)
			}
			an.InvalidateAccount(id)
		}
	case "policy":
		if a.deps.RBAC != nil {
			a.deps.RBAC.InvalidatePolicy()
		}
	case "menu":
		if a.deps.RBAC != nil {
			a.deps.RBAC.InvalidateMenu(e.Portal)
		}
	case "dict":
		if a.deps.Dict != nil {
			a.deps.Dict.Invalidate(e.Key)
		}
	case "ip":
		if a.deps.IPACL != nil {
			a.deps.IPACL.Invalidate()
		}
	}
}

// 恢复和订阅确认只清内存，不在接收协程里查库（D-075）。
func (a *App) resetInvalidationCaches() {
	for _, an := range a.authenticators {
		an.InvalidateSession("")
		an.InvalidateAccount(0)
	}
	if a.deps.RBAC != nil {
		a.deps.RBAC.InvalidatePolicy()
		a.deps.RBAC.InvalidateAllMenus()
	}
	if a.deps.Dict != nil {
		a.deps.Dict.Invalidate("")
	}
	if a.deps.IPACL != nil {
		a.deps.IPACL.Invalidate()
	}
}

func (a *App) startInvalidations() {
	if a.redis == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.invalidationCancel, a.invalidationDone = cancel, make(chan struct{})
	a.redis.OnUp(a.resetInvalidationCaches)
	go func() {
		defer close(a.invalidationDone)
		a.redis.Listen(ctx, a.receiveInvalidation, func() {
			a.resetInvalidationCaches()
			a.invalidationReady.Store(true)
		})
		a.invalidationReady.Store(false)
	}()
}
