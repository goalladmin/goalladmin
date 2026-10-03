package dict

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/textx"
)

// 本文件是字典的管理操作，供系统管理模块的接口调用。规则见 D-023：
//   - 后台新建的字典完全可编辑；编码不能含点，创建后编码和值类型不可改；
//   - 代码声明的字典本身只读，不能加项；锁定的项（代码声明的项）值不可改、不可删，显示可改、可恢复默认；
//   - 未锁定的项（后台字典的项、代码里已删掉的遗留项）可改可删；有子项的项不能删；父项创建后不可改。
// 所有写操作都在事务里、先锁住字典行，"先判断再写"不会被并发打穿（规范 §3.4）。

// DictInfo 是管理界面上的一本字典。
type DictInfo struct {
	ID        uint64            `json:"id"`
	Portal    string            `json:"portal"`
	Code      string            `json:"code"`
	Name      string            `json:"name"`
	NameI18n  map[string]string `json:"nameI18n"`
	ValueType ValueType         `json:"valueType"`
	Source    string            `json:"source"`
	Status    int               `json:"status"`
	Sort      int               `json:"sort"`
	Remark    string            `json:"remark"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// ItemInfo 是管理界面上的一项，带层级。
type ItemInfo struct {
	ID         uint64            `json:"id"`
	ParentID   uint64            `json:"parentId"`
	Value      string            `json:"value"`
	Label      string            `json:"label"`
	LabelI18n  map[string]string `json:"labelI18n"`
	Color      string            `json:"color"`
	Extra      string            `json:"extra"`
	Locked     bool              `json:"locked"`
	Overridden bool              `json:"overridden"`
	Status     int               `json:"status"`
	Sort       int               `json:"sort"`
	Remark     string            `json:"remark"`
	UpdatedAt  time.Time         `json:"updatedAt"`
	Children   []ItemInfo        `json:"children,omitempty"`
}

// DictDetail 是一本字典和它的全部项（树形）。
type DictDetail struct {
	DictInfo
	Items []ItemInfo `json:"items"`
}

// DictFilter 是列表的过滤条件；关键字匹配编码和名称。
type DictFilter struct {
	Portal string
	Source string
}

// DictInput 是新建字典的入参。
type DictInput struct {
	Code      string
	Portal    string
	Name      string
	NameI18n  map[string]string
	ValueType ValueType
	Status    *int
	Sort      int
	Remark    string
}

// DictUpdate 是修改字典的入参；编码和值类型创建后不可改。
type DictUpdate struct {
	Portal   string
	Name     string
	NameI18n map[string]string
	Status   *int
	Sort     int
	Remark   string
}

// ItemInput 是新建项的入参。
type ItemInput struct {
	ParentID  uint64
	Value     string
	Label     string
	LabelI18n map[string]string
	Color     string
	Extra     string
	Status    *int
	Sort      int
	Remark    string
}

// ItemUpdate 是修改项的入参；父项不可改。锁定的项的 Value 必须与原值相同。
type ItemUpdate struct {
	Value     string
	Label     string
	LabelI18n map[string]string
	Color     string
	Extra     string
	Status    *int
	Sort      int
	Remark    string
}

var dictSorts = map[string]string{"sort": "sort", "code": "code", "updatedAt": "updated_at"}

// field 返回带翻译键的参数错误（D-026）：key 以 dict. 或 common. 开头，msg 是英文兜底说明。
func field(name, key, msg string, params ...any) error {
	return httpx.ErrValidation.WithFields(httpx.NewField(name, key, msg, params...))
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func toInfo(d dictRow) DictInfo {
	return DictInfo{
		ID: d.ID, Portal: d.Portal, Code: d.Code, Name: d.Name, NameI18n: nonNil(decodeI18n(d.NameI18n)),
		ValueType: ValueType(d.ValueType), Source: d.Source, Status: d.Status, Sort: d.Sort, Remark: d.Remark,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

func toItemInfo(it itemRow) ItemInfo {
	return ItemInfo{
		ID: it.ID, ParentID: it.ParentID, Value: it.Value, Label: it.Label, LabelI18n: nonNil(decodeI18n(it.LabelI18n)),
		Color: it.Color, Extra: it.Extra, Locked: it.Locked, Overridden: it.Overridden, Status: it.Status, Sort: it.Sort,
		Remark: it.Remark, UpdatedAt: it.UpdatedAt,
	}
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func validStatus(p *int) (int, error) {
	if p == nil {
		return statusEnabled, nil
	}
	if *p != statusEnabled && *p != statusDisabled {
		return 0, field("status", "common.status", "must be 0 or 1")
	}
	return *p, nil
}

func (s *Service) validPortal(p string) bool { return p == AllPortals || (p != "" && s.portalOK(p)) }

// ListDicts 分页列出字典。
func (s *Service) ListDicts(ctx context.Context, q httpx.PageQuery, f DictFilter) ([]DictInfo, int64, error) {
	tx := db.From(ctx).Model(&dictRow{})
	if q.Keyword != "" {
		kw := "%" + escapeLike(q.Keyword) + "%"
		tx = tx.Where("code LIKE ? OR name LIKE ?", kw, kw)
	}
	if f.Portal != "" {
		tx = tx.Where("portal = ?", f.Portal)
	}
	if f.Source != "" {
		tx = tx.Where("source = ?", f.Source)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("dict: count: %w", err)
	}
	var rows []dictRow
	if err := tx.Order(q.Order(dictSorts, "sort ASC, id ASC")).Offset(q.Offset()).Limit(q.Limit()).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("dict: list: %w", err)
	}
	out := make([]DictInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, toInfo(r))
	}
	return out, total, nil
}

func (s *Service) dictByID(ctx context.Context, id uint64, lock bool) (*dictRow, error) {
	var d dictRow
	q := db.From(ctx)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.Where("id = ?", id).Take(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("dict: get %d: %w", id, err)
	}
	return &d, nil
}

// GetDict 返回一本字典和它的全部项。
func (s *Service) GetDict(ctx context.Context, id uint64) (*DictDetail, error) {
	d, err := s.dictByID(ctx, id, false)
	if err != nil {
		return nil, err
	}
	var items []itemRow
	if err := db.From(ctx).Where("dict_id = ?", d.ID).Order("sort ASC, id ASC").Find(&items).Error; err != nil {
		return nil, fmt.Errorf("dict: items: %w", err)
	}
	children := map[uint64][]itemRow{}
	for _, it := range items {
		children[it.ParentID] = append(children[it.ParentID], it)
	}
	var build func(parent uint64, depth int) []ItemInfo
	build = func(parent uint64, depth int) []ItemInfo {
		out := make([]ItemInfo, 0, len(children[parent]))
		for _, it := range children[parent] {
			info := toItemInfo(it)
			if depth < MaxDepth { // 库里的历史数据更深时到此为止（D-055）
				info.Children = build(it.ID, depth+1)
			}
			out = append(out, info)
		}
		return out
	}
	return &DictDetail{DictInfo: toInfo(*d), Items: build(0, 1)}, nil
}

// CreateDict 新建后台字典。
func (s *Service) CreateDict(ctx context.Context, in DictInput) (*DictInfo, error) {
	code := strings.TrimSpace(in.Code)
	if !ValidAdminCode(code) {
		return nil, field("code", "dict.code", "1-64 chars: lowercase letters, digits and underscores, starting with a letter; codes with dots are reserved for dictionaries declared in code")
	}
	vt := in.ValueType
	if vt == "" {
		vt = String
	}
	if vt != String && vt != Int {
		return nil, field("valueType", "dict.valueType", "must be string or int")
	}
	row, err := s.dictFields(in.Portal, in.Name, in.NameI18n, in.Sort, in.Remark)
	if err != nil {
		return nil, err
	}
	status, err := validStatus(in.Status)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	by := actorID(ctx)
	row.Code, row.ValueType, row.Source, row.Status = code, string(vt), SourceAdmin, status
	row.CreatedAt, row.UpdatedAt, row.CreatedBy, row.UpdatedBy = now, now, by, by
	err = db.Tx(ctx, func(ctx context.Context) error {
		var n int64
		if err := db.From(ctx).Model(&dictRow{}).Where("code = ?", code).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return httpx.ErrConflict.WithFields(httpx.NewField("code", "dict.codeTaken", "code already exists"))
		}
		if err := db.From(ctx).Create(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) { // 并发创建同一编码：唯一键兜底
				return httpx.ErrConflict.WithFields(httpx.NewField("code", "dict.codeTaken", "code already exists"))
			}
			return fmt.Errorf("dict: create: %w", err)
		}
		s.forget(ctx, code)
		return nil
	})
	if err != nil {
		return nil, err
	}
	info := toInfo(row)
	return &info, nil
}

// dictFields 校验字典的可编辑字段。
func (s *Service) dictFields(portal, name string, nameI18n map[string]string, sort int, remark string) (dictRow, error) {
	portal = strings.TrimSpace(portal)
	if portal == "" {
		portal = AllPortals
	}
	if !s.validPortal(portal) {
		return dictRow{}, field("portal", "dict.portal", "portal does not exist")
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxNameLen {
		return dictRow{}, field("name", "common.lengthRange", fmt.Sprintf("1-%d chars", maxNameLen), "min", 1, "max", maxNameLen)
	}
	if err := invisibleIn("name", name, "nameI18n", nameI18n); err != nil {
		return dictRow{}, err
	}
	ni, err := encodeI18n(nameI18n, maxNameLen)
	if err != nil {
		return dictRow{}, field("nameI18n", "dict.i18n", "invalid translations", "detail", err.Error())
	}
	if sort < 0 {
		return dictRow{}, field("sort", "common.nonNegative", "must not be negative")
	}
	if len([]rune(remark)) > 255 {
		return dictRow{}, field("remark", "common.maxLength", "at most 255 chars", "max", 255)
	}
	return dictRow{Portal: portal, Name: name, NameI18n: ni, Sort: sort, Remark: remark}, nil
}

// UpdateDict 修改后台字典；代码声明的字典返回 httpx.ErrDeclaredInCode。
func (s *Service) UpdateDict(ctx context.Context, id uint64, in DictUpdate) (*DictInfo, error) {
	f, err := s.dictFields(in.Portal, in.Name, in.NameI18n, in.Sort, in.Remark)
	if err != nil {
		return nil, err
	}
	status, err := validStatus(in.Status)
	if err != nil {
		return nil, err
	}
	err = db.Tx(ctx, func(ctx context.Context) error {
		d, err := s.dictByID(ctx, id, true)
		if err != nil {
			return err
		}
		if d.Source == SourceCode {
			return httpx.ErrDeclaredInCode
		}
		if err := db.From(ctx).Model(&dictRow{}).Where("id = ?", id).Updates(map[string]any{
			"portal": f.Portal, "name": f.Name, "name_i18n": f.NameI18n, "status": status, "sort": f.Sort, "remark": f.Remark,
			"updated_at": s.now().UTC(), "updated_by": actorID(ctx),
		}).Error; err != nil {
			return fmt.Errorf("dict: update: %w", err)
		}
		s.forget(ctx, d.Code)
		return nil
	})
	if err != nil {
		return nil, err
	}
	d, err := s.dictByID(ctx, id, false)
	if err != nil {
		return nil, err
	}
	info := toInfo(*d)
	return &info, nil
}

// DeleteDict 删除后台字典及其全部项；代码声明的字典返回 httpx.ErrDeclaredInCode。
func (s *Service) DeleteDict(ctx context.Context, id uint64) error {
	return db.Tx(ctx, func(ctx context.Context) error {
		d, err := s.dictByID(ctx, id, true)
		if err != nil {
			return err
		}
		if d.Source == SourceCode {
			return httpx.ErrDeclaredInCode
		}
		if err := db.From(ctx).Where("dict_id = ?", id).Delete(&itemRow{}).Error; err != nil {
			return fmt.Errorf("dict: delete items: %w", err)
		}
		if err := db.From(ctx).Where("id = ?", id).Delete(&dictRow{}).Error; err != nil {
			return fmt.Errorf("dict: delete: %w", err)
		}
		s.forget(ctx, d.Code)
		return nil
	})
}

// invisibleIn 检查后台写入的显示文字（主字段和各语言的文字）有没有控制字符或不可见的格式字符（D-099）。
// 只在后台写入时检查：代码声明的字典走 Sync，不经过这里。
func invisibleIn(mainField, main, i18nField string, i18n map[string]string) error {
	const msg = "must not contain control or invisible characters"
	if textx.HasInvisible(main) {
		return field(mainField, "org.textChars", msg)
	}
	for _, v := range i18n {
		if textx.HasInvisible(v) {
			return field(i18nField, "org.textChars", msg)
		}
	}
	return nil
}

// itemFields 校验项的显示字段。
func itemFields(label string, labelI18n map[string]string, color, extra string, sort int, remark string) (itemRow, error) {
	label = strings.TrimSpace(label)
	if label == "" || len([]rune(label)) > maxLabelLen {
		return itemRow{}, field("label", "common.lengthRange", fmt.Sprintf("1-%d chars", maxLabelLen), "min", 1, "max", maxLabelLen)
	}
	if err := invisibleIn("label", label, "labelI18n", labelI18n); err != nil {
		return itemRow{}, err
	}
	li, err := encodeI18n(labelI18n, maxLabelLen)
	if err != nil {
		return itemRow{}, field("labelI18n", "dict.i18n", "invalid translations", "detail", err.Error())
	}
	if !ValidColor(color) {
		return itemRow{}, field("color", "dict.color", "must be primary, success, warning, danger, info or #RRGGBB")
	}
	if len([]rune(extra)) > maxExtraLen {
		return itemRow{}, field("extra", "common.maxLength", fmt.Sprintf("at most %d chars", maxExtraLen), "max", maxExtraLen)
	}
	if sort < 0 {
		return itemRow{}, field("sort", "common.nonNegative", "must not be negative")
	}
	if len([]rune(remark)) > 255 {
		return itemRow{}, field("remark", "common.maxLength", "at most 255 chars", "max", 255)
	}
	return itemRow{Label: label, LabelI18n: li, Color: color, Extra: extra, Sort: sort, Remark: remark}, nil
}

func (s *Service) itemByID(ctx context.Context, dictID, itemID uint64) (*itemRow, error) {
	var it itemRow
	err := db.From(ctx).Where("id = ? AND dict_id = ?", itemID, dictID).Take(&it).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("dict: item %d: %w", itemID, err)
	}
	return &it, nil
}

// itemDepth 返回一项所在的层数（顶层是 1）。最多往上找 MaxDepth+1 层：库里的数据异常（成环、过深）也会停下来。
func (s *Service) itemDepth(ctx context.Context, dictID, itemID uint64) (int, error) {
	depth := 0
	for id := itemID; id != 0 && depth <= MaxDepth; depth++ {
		it, err := s.itemByID(ctx, dictID, id)
		if err != nil {
			return 0, err
		}
		id = it.ParentID
	}
	return depth, nil
}

func (s *Service) valueTaken(ctx context.Context, dictID uint64, value string, exceptID uint64) (bool, error) {
	var n int64
	// value 列是 utf8mb4_bin，按字节比较，与唯一键一致
	err := db.From(ctx).Model(&itemRow{}).Where("dict_id = ? AND value = ? AND id <> ?", dictID, value, exceptID).Count(&n).Error
	return n > 0, err
}

// CreateItem 给后台字典加一项；代码声明的字典返回 httpx.ErrDeclaredInCode。
func (s *Service) CreateItem(ctx context.Context, dictID uint64, in ItemInput) (*ItemInfo, error) {
	f, err := itemFields(in.Label, in.LabelI18n, in.Color, in.Extra, in.Sort, in.Remark)
	if err != nil {
		return nil, err
	}
	status, err := validStatus(in.Status)
	if err != nil {
		return nil, err
	}
	var row itemRow
	err = db.Tx(ctx, func(ctx context.Context) error {
		d, err := s.dictByID(ctx, dictID, true)
		if err != nil {
			return err
		}
		if d.Source == SourceCode {
			return httpx.ErrDeclaredInCode
		}
		value, err := NormalizeValue(ValueType(d.ValueType), in.Value)
		if err != nil {
			return field("value", "dict.value", "invalid value", "detail", err.Error())
		}
		// 规模上限（D-055）：字典行已经锁住，同一本字典的加项排队，数出来的就是准的
		var n int64
		if err := db.From(ctx).Model(&itemRow{}).Where("dict_id = ?", dictID).Count(&n).Error; err != nil {
			return fmt.Errorf("dict: count items: %w", err)
		}
		if n >= MaxItems {
			return field("value", "dict.tooManyItems", fmt.Sprintf("a dictionary has at most %d items", MaxItems), "max", MaxItems)
		}
		if in.ParentID != 0 {
			if _, err := s.itemByID(ctx, dictID, in.ParentID); err != nil {
				if errors.Is(err, httpx.ErrNotFound) {
					return field("parentId", "dict.parentMissing", "parent item does not exist")
				}
				return err
			}
			depth, err := s.itemDepth(ctx, dictID, in.ParentID)
			if err != nil {
				return err
			}
			if depth+1 > MaxDepth {
				return field("parentId", "dict.tooDeep", fmt.Sprintf("at most %d levels", MaxDepth), "max", MaxDepth)
			}
		}
		taken, err := s.valueTaken(ctx, dictID, value, 0)
		if err != nil {
			return err
		}
		if taken {
			return httpx.ErrConflict.WithFields(httpx.NewField("value", "dict.valueTaken", "value already exists"))
		}
		now, by := s.now().UTC(), actorID(ctx)
		row = f
		row.DictID, row.ParentID, row.Value, row.Status = dictID, in.ParentID, value, status
		row.CreatedAt, row.UpdatedAt, row.CreatedBy, row.UpdatedBy = now, now, by, by
		if err := db.From(ctx).Create(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return httpx.ErrConflict.WithFields(httpx.NewField("value", "dict.valueTaken", "value already exists"))
			}
			return fmt.Errorf("dict: create item: %w", err)
		}
		s.forget(ctx, d.Code)
		return nil
	})
	if err != nil {
		return nil, err
	}
	info := toItemInfo(row)
	return &info, nil
}

// UpdateItem 修改一项。锁定的项不能改值；改了显示字段就记为"已修改"，同步时不再覆盖。
func (s *Service) UpdateItem(ctx context.Context, dictID, itemID uint64, in ItemUpdate) (*ItemInfo, error) {
	f, err := itemFields(in.Label, in.LabelI18n, in.Color, in.Extra, in.Sort, in.Remark)
	if err != nil {
		return nil, err
	}
	status, err := validStatus(in.Status)
	if err != nil {
		return nil, err
	}
	err = db.Tx(ctx, func(ctx context.Context) error {
		d, err := s.dictByID(ctx, dictID, true)
		if err != nil {
			return err
		}
		cur, err := s.itemByID(ctx, dictID, itemID)
		if err != nil {
			return err
		}
		fields := map[string]any{
			"label": f.Label, "label_i18n": f.LabelI18n, "color": f.Color, "extra": f.Extra, "sort": f.Sort,
			"remark": f.Remark, "status": status, "updated_at": s.now().UTC(), "updated_by": actorID(ctx),
		}
		// 代码字典的值归代码（D-023）：已经不在声明里的遗留项同样不能改值，也不能重新启用（D-058）——
		// 否则把它改成任意新值再启用，代码里没有的值就能通过业务校验。遗留项只能改显示、保持停用或删除。
		if (d.Source == SourceCode || cur.Locked) && in.Value != cur.Value {
			return httpx.ErrDeclaredInCode.WithFields(httpx.NewField("value", "dict.valueLocked", "the value of an item declared in code cannot be changed"))
		}
		if d.Source == SourceCode && !cur.Locked {
			if in.Status != nil && *in.Status == statusEnabled {
				return httpx.ErrDeclaredInCode.WithFields(httpx.NewField("status", "dict.legacyItem", "an item no longer declared in code cannot be enabled"))
			}
			fields["status"] = statusDisabled
		}
		if cur.Locked {
			fields["overridden"] = s.differsFromDecl(d.Code, cur.Value, f)
		} else if in.Value != cur.Value {
			// 只在改值时校验：代码里改过值类型后遗留的旧值，仍然可以改显示、停用或删除
			value, err := NormalizeValue(ValueType(d.ValueType), in.Value)
			if err != nil {
				return field("value", "dict.value", "invalid value", "detail", err.Error())
			}
			taken, err := s.valueTaken(ctx, dictID, value, itemID)
			if err != nil {
				return err
			}
			if taken {
				return httpx.ErrConflict.WithFields(httpx.NewField("value", "dict.valueTaken", "value already exists"))
			}
			fields["value"] = value
		}
		if err := db.From(ctx).Model(&itemRow{}).Where("id = ?", itemID).Updates(fields).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return httpx.ErrConflict.WithFields(httpx.NewField("value", "dict.valueTaken", "value already exists"))
			}
			return fmt.Errorf("dict: update item: %w", err)
		}
		s.forget(ctx, d.Code)
		return nil
	})
	if err != nil {
		return nil, err
	}
	it, err := s.itemByID(ctx, dictID, itemID)
	if err != nil {
		return nil, err
	}
	info := toItemInfo(*it)
	return &info, nil
}

// differsFromDecl 报告显示字段是否与代码声明不同。找不到声明时按"已修改"处理。
func (s *Service) differsFromDecl(code, value string, f itemRow) bool {
	d, ok := s.decls[code]
	if !ok {
		return true
	}
	it, ok := declItem(d, value)
	if !ok {
		return true
	}
	li, _ := encodeI18n(it.LabelI18n, maxLabelLen)
	return it.Label != f.Label || li != f.LabelI18n || it.Color != f.Color || it.Extra != f.Extra || it.Sort != f.Sort
}

// DeleteItem 删除一项。锁定的项返回 httpx.ErrDeclaredInCode；有子项时返回冲突。
func (s *Service) DeleteItem(ctx context.Context, dictID, itemID uint64) error {
	return db.Tx(ctx, func(ctx context.Context) error {
		d, err := s.dictByID(ctx, dictID, true)
		if err != nil {
			return err
		}
		it, err := s.itemByID(ctx, dictID, itemID)
		if err != nil {
			return err
		}
		if it.Locked {
			return httpx.ErrDeclaredInCode
		}
		var n int64
		if err := db.From(ctx).Model(&itemRow{}).Where("dict_id = ? AND parent_id = ?", dictID, itemID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return httpx.NewKey(httpx.CodeConflict, "dict.hasChildren", "delete its child items first")
		}
		if err := db.From(ctx).Where("id = ?", itemID).Delete(&itemRow{}).Error; err != nil {
			return fmt.Errorf("dict: delete item: %w", err)
		}
		s.forget(ctx, d.Code)
		return nil
	})
}

// ResetItem 把代码声明的项的显示字段恢复成代码里的值。
func (s *Service) ResetItem(ctx context.Context, dictID, itemID uint64) (*ItemInfo, error) {
	err := db.Tx(ctx, func(ctx context.Context) error {
		d, err := s.dictByID(ctx, dictID, true)
		if err != nil {
			return err
		}
		cur, err := s.itemByID(ctx, dictID, itemID)
		if err != nil {
			return err
		}
		decl, ok := s.decls[d.Code]
		var it Item
		if ok && cur.Locked {
			it, ok = declItem(decl, cur.Value)
		}
		if !ok || !cur.Locked {
			return httpx.NewKey(httpx.CodeConflict, "dict.resetCodeOnly", "only items declared in code can be restored")
		}
		li, _ := encodeI18n(it.LabelI18n, maxLabelLen)
		if err := db.From(ctx).Model(&itemRow{}).Where("id = ?", itemID).Updates(map[string]any{
			"label": it.Label, "label_i18n": li, "color": it.Color, "extra": it.Extra, "sort": it.Sort,
			"overridden": false, "updated_at": s.now().UTC(), "updated_by": actorID(ctx),
		}).Error; err != nil {
			return fmt.Errorf("dict: reset item: %w", err)
		}
		s.forget(ctx, d.Code)
		return nil
	})
	if err != nil {
		return nil, err
	}
	it, err := s.itemByID(ctx, dictID, itemID)
	if err != nil {
		return nil, err
	}
	info := toItemInfo(*it)
	return &info, nil
}
