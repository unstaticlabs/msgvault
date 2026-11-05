package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SourceMergeAccessUser identifies an enabled user account whose source access
// to the historical messages changes. Other message visibility rules still apply.
type SourceMergeAccessUser struct {
	UserID      int64  `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
}

// SourceMergeAccessImpact describes current user-account access at assessment
// time. It is context for the native merge, never an additional approval gate.
// Named credentials continue to use their own configured roles.
type SourceMergeAccessImpact struct {
	AssessedAt                         time.Time               `json:"assessed_at"`
	GainingUsers                       []SourceMergeAccessUser `json:"gaining_users" doc:"Enabled non-administrator user accounts bound to the destination but not the historical source. They gain source access to the moved history."`
	LosingUsers                        []SourceMergeAccessUser `json:"losing_users" doc:"Enabled non-administrator user accounts bound to the historical source but not the destination. They lose source access to the moved history."`
	ExistingDestinationAccessUnchanged bool                    `json:"existing_destination_access_unchanged" doc:"Access to history already owned by the destination keeps following its existing source bindings."`
	BindingsCopied                     bool                    `json:"bindings_copied" doc:"The merge does not copy or rewrite user-source assignments."`
	AdministratorsKeepAccess           bool                    `json:"administrators_keep_access" doc:"Administrators retain native all-source access regardless of assignments."`
	DisabledUsersRemainDisabled        bool                    `json:"disabled_users_remain_disabled" doc:"Disabled user accounts are not current access gains or losses; they remain disabled."`
	Basis                              string                  `json:"basis" doc:"The assessment uses current stored user roles and source assignments. Native message visibility and each credential's configured role still apply."`
}

func sourceMergeAccessImpact(ctx context.Context, tx *loggedTx, from, into, messages int64) (*SourceMergeAccessImpact, error) {
	impact := &SourceMergeAccessImpact{
		AssessedAt: time.Now().UTC(), GainingUsers: []SourceMergeAccessUser{}, LosingUsers: []SourceMergeAccessUser{},
		ExistingDestinationAccessUnchanged: true, AdministratorsKeepAccess: true, DisabledUsersRemainDisabled: true,
		Basis: "Current roles and source assignments of enabled user accounts. Moved history follows destination source access. Native message visibility and each credential's configured role still apply.",
	}
	if messages == 0 {
		return impact, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.email,u.display_name,u.role,
 EXISTS(SELECT 1 FROM user_sources us WHERE us.user_id=u.id AND us.source_id=?),
 EXISTS(SELECT 1 FROM user_sources us WHERE us.user_id=u.id AND us.source_id=?)
 FROM users u WHERE u.disabled=FALSE AND u.role IN ('viewer','member')
 AND EXISTS(SELECT 1 FROM user_sources us WHERE us.user_id=u.id AND us.source_id IN (?,?))
 ORDER BY u.email,u.id`, from, into, from, into)
	if err != nil {
		return nil, fmt.Errorf("read source merge access impact: %w", err)
	}
	for rows.Next() {
		var user SourceMergeAccessUser
		var seesFrom, seesInto bool
		if err := rows.Scan(&user.UserID, &user.Email, &user.DisplayName, &user.Role, &seesFrom, &seesInto); err != nil {
			return nil, errors.Join(fmt.Errorf("scan source merge access impact: %w", err), rows.Close())
		}
		switch {
		case seesInto && !seesFrom:
			impact.GainingUsers = append(impact.GainingUsers, user)
		case seesFrom && !seesInto:
			impact.LosingUsers = append(impact.LosingUsers, user)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("read source merge access impact: %w", err)
	}
	return impact, nil
}
