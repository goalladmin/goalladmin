package org

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// Application 是入驻申请的审核视图，始终不含密码。
type Application struct {
	ID            uint64     `gorm:"column:id" json:"id"`
	Reference     string     `gorm:"column:reference" json:"reference"`
	Portal        string     `gorm:"column:portal" json:"portal"`
	Name          string     `gorm:"column:name" json:"name"`
	ContactName   string     `gorm:"column:contact_name" json:"contactName"`
	ContactPhone  string     `gorm:"column:contact_phone" json:"contactPhone"`
	OwnerUsername string     `gorm:"column:owner_username" json:"ownerUsername"`
	AgentID       uint64     `gorm:"column:agent_id" json:"agentId"`
	ReviewState   string     `gorm:"column:review_state" json:"reviewState"`
	ReviewNote    string     `gorm:"column:review_note" json:"reviewNote"`
	OrgCode       string     `gorm:"column:org_code" json:"orgCode"`
	CreatedAt     time.Time  `gorm:"column:created_at" json:"createdAt"`
	ReviewedAt    *time.Time `gorm:"column:reviewed_at" json:"reviewedAt"`
	ReviewedBy    uint64     `gorm:"column:reviewed_by" json:"reviewedBy"`
}

// Invitation 不暴露邀请凭证，原始凭证只在创建时返回。
type Invitation struct {
	ID        uint64     `gorm:"column:id" json:"id"`
	TokenHash string     `gorm:"column:token_hash" json:"-"`
	AgentID   uint64     `gorm:"column:agent_id" json:"agentId"`
	ExpiresAt time.Time  `gorm:"column:expires_at" json:"expiresAt"`
	UsedAt    *time.Time `gorm:"column:used_at" json:"usedAt"`
	RevokedAt *time.Time `gorm:"column:revoked_at" json:"revokedAt"`
	CreatedAt time.Time  `gorm:"column:created_at" json:"createdAt"`
	CreatedBy uint64     `gorm:"column:created_by" json:"createdBy"`
}

// ApplicationInput 只接收申请资料，归属由邀请决定。
type ApplicationInput struct {
	Name            string `json:"name"`
	ContactName     string `json:"contactName"`
	ContactPhone    string `json:"contactPhone"`
	OwnerUsername   string `json:"ownerUsername"`
	InvitationToken string `json:"invitationToken"`
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func invitationHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func invalidInvitation() error {
	return fieldErr("invitationToken", "onboarding.invitation", "the invitation is unavailable")
}

// Apply 创建待审记录，不创建主体或账号。验证码及来源限流由入口在调用前完成。
func (s *Service) Apply(ctx context.Context, k Kind, in ApplicationInput) (*Application, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	fields, user, _, err := cleanCreate(k, CreateInput{Name: in.Name, ContactName: in.ContactName, ContactPhone: in.ContactPhone, OwnerUsername: in.OwnerUsername})
	if err != nil {
		return nil, err
	}
	if fields.contactName == "" || fields.contactPhone == "" {
		return nil, fieldErr("contactPhone", "onboarding.contact", "contact name and phone are required")
	}
	ref, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	row := Application{Reference: ref, Portal: k.portal, Name: fields.name, ContactName: fields.contactName, ContactPhone: fields.contactPhone, OwnerUsername: user, ReviewState: "pending", CreatedAt: s.now().UTC()}
	err = db.Tx(ctx, func(ctx context.Context) error {
		if in.InvitationToken != "" {
			if k != Merchant() || len(in.InvitationToken) != 64 {
				return invalidInvitation()
			}
			var invite Invitation
			q := db.From(ctx).Table("ga_org_invitation")
			if err := q.Where("token_hash = ?", invitationHash(in.InvitationToken)).Take(&invite).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return invalidInvitation()
				}
				return err
			}
			// 和代理商 WithActor 的顺序一致：先主体，再邀请。锁后重读有效期与消费状态。
			agent, err := s.lockOrg(ctx, Agent(), invite.AgentID)
			if err != nil {
				return err
			}
			if agent.Status != StatusEnabled {
				return invalidInvitation()
			}
			if err := q.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", invite.ID).Take(&invite).Error; err != nil {
				return err
			}
			if invite.UsedAt != nil || invite.RevokedAt != nil || !s.now().Before(invite.ExpiresAt) {
				return invalidInvitation()
			}
			row.AgentID = invite.AgentID
			if err := q.Where("id = ?", invite.ID).Update("used_at", s.now().UTC()).Error; err != nil {
				return err
			}
		}
		return db.From(ctx).Table("ga_org_application").Create(&row).Error
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Applications 分页读取平台审核列表。
func (s *Service) Applications(ctx context.Context, state string, q httpx.PageQuery) ([]Application, int64, error) {
	tx := db.From(ctx).Table("ga_org_application")
	if state != "" {
		tx = tx.Where("review_state = ?", state)
	}
	if q.Keyword != "" {
		like := "%" + escapeLike(q.Keyword) + "%"
		tx = tx.Where("reference LIKE ? OR name LIKE ?", like, like)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []Application{}
	err := tx.Order("id DESC").Offset(q.Offset()).Limit(q.Limit()).Find(&rows).Error
	return rows, total, err
}

// CheckApplicationReview 在计算初始密码前检查申请存在、待审和说明有效；锁内仍重新核对。
func (s *Service) CheckApplicationReview(ctx context.Context, id uint64, approve bool, note string) error {
	if _, err := text("note", note, MaxRemark, false); err != nil {
		return err
	}
	if !approve && strings.TrimSpace(note) == "" {
		return fieldErr("note", "onboarding.note", "a rejection reason is required")
	}
	var row Application
	if err := db.From(ctx).Table("ga_org_application").Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return httpx.ErrNotFound
		}
		return err
	}
	if row.ReviewState != "pending" {
		return fieldErr("id", "onboarding.reviewed", "the application has already been reviewed")
	}
	return nil
}

// ReviewApplication 必须由平台 WithActor 调用。审核状态、主体和账号在同一事务提交。
// pwd 仅通过时需要，在拿锁前计算。已处理的申请不能重复通过或驳回。
func (s *Service) ReviewApplication(ctx context.Context, actor auth.Principal, id uint64, approve bool, note string, pwd InitialPassword) (*Created, error) {
	if actor.Portal != "platform" || actor.OrgID != 0 || actor.UserID == 0 {
		return nil, httpx.ErrForbidden
	}
	note, err := text("note", note, MaxRemark, false)
	if err != nil {
		return nil, err
	}
	if !approve && strings.TrimSpace(note) == "" {
		return nil, fieldErr("note", "onboarding.note", "a rejection reason is required")
	}
	var out *Created
	err = db.Tx(ctx, func(ctx context.Context) error {
		var row Application
		tx := db.From(ctx).Table("ga_org_application")
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return httpx.ErrNotFound
			}
			return err
		}
		if row.ReviewState != "pending" {
			return fieldErr("id", "onboarding.reviewed", "the application has already been reviewed")
		}
		state, code := "rejected", ""
		if approve {
			kind := Agent()
			if row.Portal == "merchant" {
				kind = Merchant()
			} else if row.Portal != "agent" {
				return errKind
			}
			if row.AgentID != 0 {
				a, err := s.lockOrg(ctx, Agent(), row.AgentID)
				if err != nil {
					return err
				}
				if a.Status != StatusEnabled {
					return invalidInvitation()
				}
			}
			var err error
			out, err = s.Create(ctx, kind, CreateInput{Name: row.Name, ContactName: row.ContactName, ContactPhone: row.ContactPhone, OwnerUsername: row.OwnerUsername, AgentID: row.AgentID}, pwd, actor.UserID)
			if err != nil {
				return err
			}
			state, code = "approved", out.Org.Code
		}
		return tx.Where("id = ?", id).Updates(map[string]any{"review_state": state, "review_note": note, "org_code": code, "reviewed_at": s.now().UTC(), "reviewed_by": actor.UserID}).Error
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func invitationActor(actor auth.Principal) error {
	if actor.Portal != "agent" || actor.OrgID == 0 || !actor.Super {
		return httpx.ErrForbidden
	}
	return nil
}

// CreateInvitation 由代理商 WithActor 调用，actor 必须是锁内重新认定的主账号。
func (s *Service) CreateInvitation(ctx context.Context, actor auth.Principal) (*Invitation, string, error) {
	if err := invitationActor(actor); err != nil {
		return nil, "", err
	}
	token, err := randomHex(32)
	if err != nil {
		return nil, "", err
	}
	now := s.now().UTC()
	row := Invitation{AgentID: actor.OrgID, TokenHash: invitationHash(token), ExpiresAt: now.Add(7 * 24 * time.Hour), CreatedAt: now, CreatedBy: actor.UserID}
	if err := db.From(ctx).Table("ga_org_invitation").Create(&row).Error; err != nil {
		return nil, "", err
	}
	return &row, token, nil
}

// Invitations 只读当前代理商的邀请；使用 LiveAuth 的主账号入口。
func (s *Service) Invitations(ctx context.Context, actor auth.Principal, q httpx.PageQuery) ([]Invitation, int64, error) {
	if err := invitationActor(actor); err != nil {
		return nil, 0, err
	}
	tx := db.From(ctx).Table("ga_org_invitation").Where("agent_id = ?", actor.OrgID)
	var n int64
	if err := tx.Count(&n).Error; err != nil {
		return nil, 0, err
	}
	rows := []Invitation{}
	err := tx.Order("id DESC").Offset(q.Offset()).Limit(q.Limit()).Find(&rows).Error
	return rows, n, err
}

// RevokeInvitation 由 WithActor 调用，已提交的申请不受影响。已经撤销过的直接返回成功，不改第一次的撤销时间（D-099）；
// 别的代理商的、不存在的邀请回 404。
func (s *Service) RevokeInvitation(ctx context.Context, actor auth.Principal, id uint64) error {
	if err := invitationActor(actor); err != nil {
		return err
	}
	return db.Tx(ctx, func(ctx context.Context) error {
		var row Invitation
		err := db.From(ctx).Table("ga_org_invitation").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND agent_id = ?", id, actor.OrgID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.RevokedAt != nil {
			return nil
		}
		return db.From(ctx).Table("ga_org_invitation").Where("id = ? AND agent_id = ?", id, actor.OrgID).
			Update("revoked_at", s.now().UTC()).Error
	})
}
