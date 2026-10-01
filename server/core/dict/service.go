package dict

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/ttlcache"
)

// Service 是字典服务，通过 app.Deps.Dict 注入模块。
//
// 读取（Get、Many、Has、Label）带进程内缓存；写入在事务提交后清缓存（db.AfterCommit）。
type Service struct {
	log      *slog.Logger
	now      func() time.Time
	portalOK func(string) bool
	cache    *ttlcache.Cache[*loaded]
	decls    map[string]Dict // 代码声明，编码 → 声明；Sync 时填入，供"恢复默认"和"是否改过"判断
	// 缓存回填靠 cache 自己的失效代数（Gen / SetIfGen）：写入方 Delete / Flush 之后，读方读库前记下的代数就作废，
	// 不会把"读到旧行 → 写入方清缓存 → 读方把旧行填回去"的旧数据再留一个 TTL（D-043）。
	testHook func(stage string) // 只给本包测试用（生产为 nil）：在读库之后、回填之前插入另一次写入
}

// Options 是构造参数。
type Options struct {
	Log      *slog.Logger
	Now      func() time.Time
	CacheTTL time.Duration
	// PortalOK 报告端代号是否已注册，后台新建字典时校验。为 nil 时只接受 AllPortals。
	PortalOK func(string) bool
}

// NewService 创建字典服务。
func NewService(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = time.Minute
	}
	if o.PortalOK == nil {
		o.PortalOK = func(string) bool { return false }
	}
	return &Service{
		log: o.Log.With("component", "dict"), now: o.Now, portalOK: o.PortalOK,
		cache: ttlcache.New[*loaded](o.CacheTTL, o.Now), decls: map[string]Dict{},
	}
}

func (s *Service) hook(stage string) {
	if s.testHook != nil {
		s.testHook(stage)
	}
}

// loaded 是缓存里的一本字典：字典行 + 全部项（按 sort、id 排序）。nil 表示不存在。
type loaded struct {
	d     dictRow
	items []itemRow
}

// load 读一本字典（带缓存）。不存在时返回 nil，不缓存"不存在"：格式合法的编码有无数种，缓存它们只会占内存。
func (s *Service) load(ctx context.Context, code string) (*loaded, error) {
	if l, ok := s.cache.Get(code); ok {
		return l, nil
	}
	// 读库前记下缓存的失效代数：读库期间有写入提交并清过缓存（forget）就不回填。
	// 用缓存自己的 SetIfGen 而不是"查代数再 Set"两步：两步之间写入方仍可能清缓存，旧值会被填回去（D-043）。
	g := s.cache.Gen()
	var d dictRow
	err := db.From(ctx).Where("code = ?", code).Take(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dict: load %s: %w", code, err)
	}
	var items []itemRow
	if err := db.From(ctx).Where("dict_id = ?", d.ID).Order("sort ASC, id ASC").Find(&items).Error; err != nil {
		return nil, fmt.Errorf("dict: load items %s: %w", code, err)
	}
	l := &loaded{d: d, items: items}
	s.hook("dict.read")
	if !db.InTx(ctx) { // 事务快照可能早于取代数的那一刻，不能拿它回填
		s.cache.SetIfGen(code, l, g)
	}
	return l, nil
}

// forget 在事务提交后清掉某本字典的缓存。
func (s *Service) forget(ctx context.Context, codes ...string) {
	db.AfterCommit(ctx, func() {
		for _, c := range codes {
			s.cache.Delete(c)
		}
	})
}

func actorID(ctx context.Context) uint64 {
	if p, ok := auth.FromCtx(ctx); ok {
		return p.UserID
	}
	return 0
}

// ---- 读取 ----

// View 是读取接口返回给前端的一本字典，文字已按语言选好。
type View struct {
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	ValueType ValueType  `json:"valueType"`
	Items     []ViewItem `json:"items"`
}

// ViewItem 是一项。Value 按字典的值类型输出为 JSON 字符串或数字。
// 停用的项也返回（Status 为 0），历史数据要靠它显示文字；前端下拉只列启用的项。
type ViewItem struct {
	Value    any        `json:"value"`
	Label    string     `json:"label"`
	Color    string     `json:"color,omitempty"`
	Extra    string     `json:"extra,omitempty"`
	Status   int        `json:"status"`
	Children []ViewItem `json:"children,omitempty"`
}

// visible 报告字典对某端是否可见：启用中，且属于该端或所有端共用。
func (l *loaded) visible(portal string) bool {
	return l != nil && l.d.Status == statusEnabled && (l.d.Portal == AllPortals || l.d.Portal == portal)
}

// Get 返回某端可见的一本字典，文字按 lang 选择。不存在、已停用或属于别的端时返回 httpx.ErrNotFound。
func (s *Service) Get(ctx context.Context, portal, code, lang string) (*View, error) {
	if !ValidCode(code) {
		return nil, httpx.ErrNotFound
	}
	l, err := s.load(ctx, code)
	if err != nil {
		return nil, err
	}
	if !l.visible(portal) {
		return nil, httpx.ErrNotFound
	}
	return l.view(lang), nil
}

// Many 批量读取，看不到的编码直接略过。
func (s *Service) Many(ctx context.Context, portal string, codes []string, lang string) (map[string]*View, error) {
	out := make(map[string]*View, len(codes))
	for _, c := range codes {
		v, err := s.Get(ctx, portal, c, lang)
		if errors.Is(err, httpx.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[c] = v
	}
	return out, nil
}

// Has 报告字典里是否有这个值且处于启用状态。业务代码用它校验入参；不区分端。
func (s *Service) Has(ctx context.Context, code, value string) (bool, error) {
	if !ValidCode(code) {
		return false, nil
	}
	l, err := s.load(ctx, code)
	if err != nil || l == nil || l.d.Status != statusEnabled {
		return false, err
	}
	for _, it := range l.items {
		if it.Value == value {
			return l.status(it) == statusEnabled, nil
		}
	}
	return false, nil
}

// status 是一项实际生效的状态：代码字典里已经不在声明中的项（同步时解除了锁定）一律按停用算（D-058）。
// 代码拥有值，后台不能让代码里没有的值重新生效；库里即使被改成了启用，也不认。
func (l *loaded) status(it itemRow) int {
	if l.d.Source == SourceCode && !it.Locked {
		return statusDisabled
	}
	return it.Status
}

// Label 返回值的显示文字（停用的项也返回）；找不到时返回值本身。
func (s *Service) Label(ctx context.Context, code, value, lang string) string {
	if !ValidCode(code) {
		return value
	}
	l, err := s.load(ctx, code)
	if err != nil || l == nil {
		return value
	}
	for _, it := range l.items {
		if it.Value == value {
			return pick(it.Label, it.LabelI18n, lang)
		}
	}
	return value
}

func (l *loaded) view(lang string) *View {
	vt := ValueType(l.d.ValueType)
	children := map[uint64][]itemRow{}
	for _, it := range l.items {
		children[it.ParentID] = append(children[it.ParentID], it)
	}
	var build func(parent uint64, depth int) []ViewItem
	build = func(parent uint64, depth int) []ViewItem {
		rows := children[parent]
		out := make([]ViewItem, 0, len(rows))
		for _, it := range rows {
			v := ViewItem{
				Value: typedValue(vt, it.Value), Label: pick(it.Label, it.LabelI18n, lang),
				Color: it.Color, Extra: it.Extra, Status: l.status(it),
			}
			if depth < MaxDepth { // 库里的历史数据更深时到此为止（D-055）
				v.Children = build(it.ID, depth+1)
			}
			out = append(out, v)
		}
		return out
	}
	return &View{Code: l.d.Code, Name: pick(l.d.Name, l.d.NameI18n, lang), ValueType: vt, Items: build(0, 1)}
}

func typedValue(vt ValueType, v string) any {
	if vt == Int {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return v
}

// ---- 同步代码声明 ----

// 等同步锁的总时长和两次尝试的间隔。
const (
	syncLockWait = 30 * time.Second
	syncLockPoll = 200 * time.Millisecond
)

// Sync 把代码声明的字典同步进库。启动时由 app 调用一次（声明须先经 ValidateDecls 校验）。
//
//   - 字典本身完全以代码为准；
//   - 代码里的项：没有就插入；有就更新层级，显示字段只在后台没改过（overridden = 0）时更新；启用状态不动；
//   - 库里有、代码里没有的项：解除锁定并停用（历史数据还要靠它显示文字），打警告；
//   - 库里标记为代码来源、但已没有模块声明的字典：转为后台字典，打警告，由管理员决定是否删除。
func (s *Service) Sync(ctx context.Context, decls []Decl) error {
	// 同步自己开事务，并且要独占一条连接来持有会话级锁：在外层事务里调用会让 SQL 走外层事务的连接，锁却在另一条上
	if db.InTx(ctx) {
		return errors.New("dict: Sync 不能在事务里调用")
	}
	// 多个实例同时启动时串行同步：用 MySQL 的会话级锁，和迁移器的做法一样。
	// 不让 GET_LOCK 在服务端等（那会被连接串的读超时掐断，D-037），而是每隔一会儿试一次、不等待。
	return db.From(ctx).Connection(func(conn *gorm.DB) error {
		// conn 是不克隆的句柄，直接在它上面 Raw / Exec 会把语句状态（Model、Dest）留在句柄里，
		// 之后拿它当基础句柄开事务会带着这些残留出错；所以只用一个会克隆的会话，conn 本身不碰。
		c := conn.Session(&gorm.Session{NewDB: true})
		deadline := time.Now().Add(syncLockWait)
		for {
			var got *int
			if err := c.Raw("SELECT GET_LOCK('ga_dict_sync', 0)").Scan(&got).Error; err != nil {
				return fmt.Errorf("dict: 获取同步锁: %w", err)
			}
			if got != nil && *got == 1 {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("dict: 30 秒内未获得同步锁，可能有另一个实例正在同步")
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("dict: 获取同步锁: %w", ctx.Err())
			case <-time.After(syncLockPoll):
			}
		}
		defer func() { _ = c.Exec("DO RELEASE_LOCK('ga_dict_sync')").Error }()
		// 同步的 SQL 全部走这条拿着锁的连接：会话级锁占着一条连接，再从连接池取第二条来跑事务，
		// 在 maxOpenConns 为 1 的部署上会取不到连接、启动卡死（D-043）
		return s.sync(db.WithDB(ctx, c), decls)
	})
}

func (s *Service) sync(ctx context.Context, decls []Decl) error {
	s.decls = make(map[string]Dict, len(decls))
	codes := make([]string, 0, len(decls))
	for _, d := range decls {
		s.decls[d.Dict.Code] = d.Dict
		codes = append(codes, d.Dict.Code)
	}
	sort.Strings(codes)
	for _, c := range codes {
		d := s.decls[c]
		if err := db.Tx(ctx, func(ctx context.Context) error { return s.syncOne(ctx, d) }); err != nil {
			return fmt.Errorf("dict: 同步 %s: %w", c, err)
		}
	}
	var orphans []dictRow
	q := db.From(ctx).Where("source = ?", SourceCode)
	if len(codes) > 0 {
		q = q.Where("code NOT IN ?", codes)
	}
	if err := q.Find(&orphans).Error; err != nil {
		return fmt.Errorf("dict: 查询不再声明的字典: %w", err)
	}
	for _, o := range orphans {
		err := db.Tx(ctx, func(ctx context.Context) error {
			now := s.now().UTC()
			if err := db.From(ctx).Model(&dictRow{}).Where("id = ?", o.ID).
				Updates(map[string]any{"source": SourceAdmin, "updated_at": now}).Error; err != nil {
				return err
			}
			return db.From(ctx).Model(&itemRow{}).Where("dict_id = ?", o.ID).
				Updates(map[string]any{"locked": false, "overridden": false, "updated_at": now}).Error
		})
		if err != nil {
			return fmt.Errorf("dict: 释放字典 %s: %w", o.Code, err)
		}
		s.log.Warn("字典已没有模块声明，转为后台字典；不再需要时请在字典管理里删除", "code", o.Code)
	}
	s.cache.Flush()
	return nil
}

func (s *Service) syncOne(ctx context.Context, d Dict) error {
	now := s.now().UTC()
	vt := d.ValueType
	if vt == "" {
		vt = String
	}
	nameI18n, _ := encodeI18n(d.NameI18n, maxNameLen)
	var row dictRow
	err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("code = ?", d.Code).Take(&row).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = dictRow{
			Portal: d.Portal, Code: d.Code, Name: d.Name, NameI18n: nameI18n, ValueType: string(vt), Source: SourceCode,
			Status: statusEnabled, Sort: d.Sort, Remark: d.Remark, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.From(ctx).Create(&row).Error; err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := db.From(ctx).Model(&dictRow{}).Where("id = ?", row.ID).Updates(map[string]any{
			"portal": d.Portal, "name": d.Name, "name_i18n": nameI18n, "value_type": string(vt), "source": SourceCode,
			"status": statusEnabled, "sort": d.Sort, "remark": d.Remark, "updated_at": now,
		}).Error; err != nil {
			return err
		}
	}

	var existing []itemRow
	if err := db.From(ctx).Where("dict_id = ?", row.ID).Find(&existing).Error; err != nil {
		return err
	}
	byValue := make(map[string]*itemRow, len(existing))
	for i := range existing {
		byValue[existing[i].Value] = &existing[i]
	}
	declared := map[string]bool{}

	var walk func(parent uint64, items []Item) error
	walk = func(parent uint64, items []Item) error {
		for _, it := range items {
			k := it.Value
			declared[k] = true
			li18n, _ := encodeI18n(it.LabelI18n, maxLabelLen)
			var id uint64
			if cur, ok := byValue[k]; ok {
				fields := map[string]any{"parent_id": parent, "locked": true, "updated_at": now}
				if !cur.Locked || !cur.Overridden {
					// 之前解除过锁定的项重新被声明：显示以代码为准
					fields["label"], fields["label_i18n"], fields["color"], fields["extra"], fields["sort"] = it.Label, li18n, it.Color, it.Extra, it.Sort
					fields["overridden"] = false
				}
				if !cur.Locked {
					fields["status"] = statusEnabled // 曾被移出声明、现在又回来了：恢复启用
				}
				if err := db.From(ctx).Model(&itemRow{}).Where("id = ?", cur.ID).Updates(fields).Error; err != nil {
					return err
				}
				id = cur.ID
			} else {
				r := itemRow{
					DictID: row.ID, ParentID: parent, Value: it.Value, Label: it.Label, LabelI18n: li18n, Color: it.Color,
					Extra: it.Extra, Locked: true, Status: statusEnabled, Sort: it.Sort, CreatedAt: now, UpdatedAt: now,
				}
				if err := db.From(ctx).Create(&r).Error; err != nil {
					return err
				}
				id = r.ID
			}
			if err := walk(id, it.Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(0, d.Items); err != nil {
		return err
	}

	for _, it := range existing {
		if declared[it.Value] {
			continue
		}
		if !it.Locked && it.Status == statusDisabled {
			continue
		}
		if err := db.From(ctx).Model(&itemRow{}).Where("id = ?", it.ID).
			Updates(map[string]any{"locked": false, "overridden": false, "status": statusDisabled, "updated_at": now}).Error; err != nil {
			return err
		}
		s.log.Warn("字典项已不在代码声明里，已解除锁定并停用；确认没有数据再用它后可以在字典管理里删除", "code", d.Code, "value", it.Value)
	}
	return nil
}

// declItem 在代码声明里按值查找一项。
func declItem(d Dict, value string) (Item, bool) {
	var found Item
	var ok bool
	var walk func(items []Item)
	walk = func(items []Item) {
		for _, it := range items {
			if ok {
				return
			}
			if it.Value == value {
				found, ok = it, true
				return
			}
			walk(it.Children)
		}
	}
	walk(d.Items)
	return found, ok
}
