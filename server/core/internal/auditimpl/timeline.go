package auditimpl

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/internal/oplogimpl"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
)

// 调查时间线（D-032 第 6 条）：三张表各取一页，合并后按时间倒序截取。
// 排序键是（时间，类型，ID），翻页游标记住上一页最后一条的这三样，同一毫秒的记录也不会漏掉或重复。

const (
	defaultTimelineLimit = 100
	maxTimelineLimit     = 200
)

// typeRank 是同一时刻各类型的先后：安全事件、操作、登录。
func typeRank(t string) int {
	switch t {
	case audit.TimelineSecurity:
		return 3
	case audit.TimelineOperation:
		return 2
	case audit.TimelineLogin:
		return 1
	}
	return 0
}

type cursor struct {
	at   time.Time
	rank int
	id   uint64
}

func (c cursor) String(typ string) string {
	return c.at.UTC().Format(time.RFC3339Nano) + "," + typ + "," + strconv.FormatUint(c.id, 10)
}

func parseCursor(s string) (cursor, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return cursor{}, audit.ErrBadCursor
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return cursor{}, audit.ErrBadCursor
	}
	// 换算成 UTC 后年份超出 1–9999 的时间数据库驱动不接受（D-099）
	if y := at.UTC().Year(); y < 1 || y > 9999 {
		return cursor{}, audit.ErrBadCursor
	}
	rank := typeRank(parts[1])
	id, err := strconv.ParseUint(parts[2], 10, 64)
	if rank == 0 || err != nil {
		return cursor{}, audit.ErrBadCursor
	}
	return cursor{at: at.UTC(), rank: rank, id: id}, nil
}

// later 报告 a 是否排在 b 前面（更晚）。
func later(a, b audit.TimelineItem) bool {
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	if ra, rb := typeRank(a.Type), typeRank(b.Type); ra != rb {
		return ra > rb
	}
	return a.ID > b.ID
}

// Timeline 实现 audit.Service。
func (r *Recorder) Timeline(ctx context.Context, q audit.TimelineQuery) (audit.Timeline, error) {
	n := 0
	for _, set := range []bool{q.UserID != 0, q.IP != "", q.SessionID != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return audit.Timeline{}, audit.ErrBadTimelineQuery
	}
	var cur *cursor
	if q.Cursor != "" {
		c, err := parseCursor(q.Cursor)
		if err != nil {
			return audit.Timeline{}, err
		}
		cur = &c
	}
	if r.db == nil {
		return audit.Timeline{}, errors.New("audit: no database")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultTimelineLimit
	}
	if limit > maxTimelineLimit {
		limit = maxTimelineLimit
	}
	// 每张表多取一条：合并后超过 limit 就说明还有更早的
	fetch := limit + 1
	scope := func(db *gorm.DB, typ, timeCol string) *gorm.DB {
		switch {
		case q.Portal != "" && q.IncludeGlobal && typ == audit.TimelineSecurity:
			db = db.Where("portal IN (?, '')", q.Portal)
		case q.Portal != "":
			db = db.Where("portal = ?", q.Portal)
		}
		switch {
		case q.UserID != 0:
			db = db.Where("user_id = ?", q.UserID)
		case q.IP != "":
			db = db.Where("ip = ?", q.IP)
		default:
			db = db.Where("session_id = ?", q.SessionID)
		}
		if cur != nil {
			switch rank := typeRank(typ); {
			case rank < cur.rank:
				db = db.Where(timeCol+" <= ?", cur.at)
			case rank > cur.rank:
				db = db.Where(timeCol+" < ?", cur.at)
			default:
				db = db.Where("("+timeCol+" < ? OR ("+timeCol+" = ? AND id < ?))", cur.at, cur.at, cur.id)
			}
		}
		return db.Order(timeCol + " DESC, id DESC").Limit(fetch)
	}
	base := r.db.WithContext(ctx)

	var logins []session.LoginLog
	if err := scope(base.Model(&session.LoginLog{}), audit.TimelineLogin, "created_at").Find(&logins).Error; err != nil {
		return audit.Timeline{}, err
	}
	var ops []oplogimpl.Entry
	if err := scope(base.Model(&oplogimpl.Entry{}), audit.TimelineOperation, "created_at").Omit("body", "query").Find(&ops).Error; err != nil {
		return audit.Timeline{}, err
	}
	var events []securityRow
	if err := scope(base.Model(&securityRow{}), audit.TimelineSecurity, "first_at").Find(&events).Error; err != nil {
		return audit.Timeline{}, err
	}

	items := make([]audit.TimelineItem, 0, len(logins)+len(ops)+len(events))
	for _, l := range logins {
		ok := l.Success
		items = append(items, audit.TimelineItem{
			Type: audit.TimelineLogin, ID: l.ID, At: l.CreatedAt, UserID: l.UserID, Username: l.Username, SessionID: l.SessionID,
			IP: l.IP, UserAgent: l.UserAgent, RequestID: l.RequestID, Success: &ok, Reason: l.Reason,
		})
	}
	for _, o := range ops {
		code := o.Code
		items = append(items, audit.TimelineItem{
			Type: audit.TimelineOperation, ID: o.ID, At: o.CreatedAt, UserID: o.UserID, Username: o.Username, SessionID: o.SessionID,
			IP: o.IP, UserAgent: o.UserAgent, RequestID: o.RequestID, Action: o.Action, Method: o.Method, Path: o.Path,
			HTTPStatus: o.HTTPStatus, Code: &code, Error: o.Error,
		})
	}
	for _, e := range events {
		last := e.LastAt
		items = append(items, audit.TimelineItem{
			Type: audit.TimelineSecurity, ID: e.ID, At: e.FirstAt, UserID: e.UserID, Username: e.Username, SessionID: e.SessionID,
			IP: e.IP, UserAgent: e.UserAgent, RequestID: e.RequestID, Kind: e.Kind, Level: e.Level, Detail: e.Detail,
			Method: e.Method, Path: e.Path, Count: e.Count, LastAt: &last,
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return later(items[i], items[j]) })
	out := audit.Timeline{Items: items}
	if len(items) > limit {
		out.Items, out.More = items[:limit], true
		last := out.Items[limit-1]
		out.Next = cursor{at: last.At, id: last.ID}.String(last.Type)
	}
	return out, nil
}
