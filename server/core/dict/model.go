package dict

import "time"

// dictRow 对应 ga_dict。
type dictRow struct {
	ID        uint64    `gorm:"column:id;primaryKey"`
	Portal    string    `gorm:"column:portal"`
	Code      string    `gorm:"column:code"`
	Name      string    `gorm:"column:name"`
	NameI18n  string    `gorm:"column:name_i18n"`
	ValueType string    `gorm:"column:value_type"`
	Source    string    `gorm:"column:source"`
	Status    int       `gorm:"column:status"`
	Sort      int       `gorm:"column:sort"`
	Remark    string    `gorm:"column:remark"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	CreatedBy uint64    `gorm:"column:created_by"`
	UpdatedBy uint64    `gorm:"column:updated_by"`
}

func (dictRow) TableName() string { return "ga_dict" }

// itemRow 对应 ga_dict_item。
type itemRow struct {
	ID         uint64    `gorm:"column:id;primaryKey"`
	DictID     uint64    `gorm:"column:dict_id"`
	ParentID   uint64    `gorm:"column:parent_id"`
	Value      string    `gorm:"column:value"`
	Label      string    `gorm:"column:label"`
	LabelI18n  string    `gorm:"column:label_i18n"`
	Color      string    `gorm:"column:color"`
	Extra      string    `gorm:"column:extra"`
	Locked     bool      `gorm:"column:locked"`
	Overridden bool      `gorm:"column:overridden"`
	Status     int       `gorm:"column:status"`
	Sort       int       `gorm:"column:sort"`
	Remark     string    `gorm:"column:remark"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
	CreatedBy  uint64    `gorm:"column:created_by"`
	UpdatedBy  uint64    `gorm:"column:updated_by"`
}

func (itemRow) TableName() string { return "ga_dict_item" }

const (
	statusEnabled  = 1
	statusDisabled = 0
)
