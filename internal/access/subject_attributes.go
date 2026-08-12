package access

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/platform/database"
)

// Subject attributes are the business facts about a reader that governed row
// access policies match against - which regions they cover, which cost centre
// they own.
//
// They are administered, never self-asserted: a value a person could set for
// themselves would make the policy that reads it worthless. Granting one is
// therefore an administrator action and is audited like any other permission
// change.
var (
	ErrSubjectAttributeInvalid   = errors.New("subject attribute request is invalid")
	ErrSubjectAttributeForbidden = errors.New("only a platform administrator or the user's domain administrator can grant subject attributes")
)

var subjectAttributeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type SubjectAttribute struct {
	UserID      string   `json:"userId"`
	Key         string   `json:"attributeKey"`
	Values      []string `json:"attributeValues"`
	GrantedBy   string   `json:"grantedBy"`
	UpdatedAt   string   `json:"updatedAt"`
	DisplayName string   `json:"displayName,omitempty"`
}

// normalizeAttributeValues trims, deduplicates and sorts so the same grant
// always stores the same bytes, and so a value list cannot smuggle blanks.
func normalizeAttributeValues(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\n\r\t") {
			return nil, ErrSubjectAttributeInvalid
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) < 1 || len(result) > 256 {
		return nil, ErrSubjectAttributeInvalid
	}
	sort.Strings(result)
	return result, nil
}

// requireSubjectAttributeAdministratorTx allows a platform administrator, or a
// domain administrator of a domain the target user actually belongs to. A
// domain administrator can therefore scope their own members and nobody else.
func requireSubjectAttributeAdministratorTx(
	ctx context.Context, tx pgx.Tx, actorID, targetUserID string,
) error {
	platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, actorID)
	if err != nil {
		return err
	}
	if platformAdministrator {
		return nil
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.domain_memberships AS target
		WHERE target.user_id=$2::uuid AND target.status='ACTIVE'
		  AND platform.user_is_domain_administrator(target.domain_id)
	)`, actorID, targetUserID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrSubjectAttributeForbidden
	}
	return nil
}

// ListSubjectAttributes returns one user's grants.
func (s *AdminStore) ListSubjectAttributes(
	ctx context.Context, tenantID, actorID, userID string,
) (items []SubjectAttribute, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := requireSubjectAttributeAdministratorTx(ctx, tx, actorID, userID); err != nil {
			return err
		}
		rows, queryErr := tx.Query(ctx, `SELECT attribute.user_id::text,attribute.attribute_key,
			attribute.attribute_values,attribute.granted_by::text,attribute.updated_at::text,
			subject.display_name
		FROM platform.subject_attributes AS attribute
		JOIN platform.users AS subject
		  ON subject.id=attribute.user_id AND subject.tenant_id=attribute.tenant_id
		WHERE attribute.tenant_id=$1::uuid AND attribute.user_id=$2::uuid
		ORDER BY attribute.attribute_key`, tenantID, userID)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item SubjectAttribute
			if scanErr := rows.Scan(&item.UserID, &item.Key, &item.Values,
				&item.GrantedBy, &item.UpdatedAt, &item.DisplayName); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// SetSubjectAttribute grants or replaces one attribute for one user.
func (s *AdminStore) SetSubjectAttribute(
	ctx context.Context, tenantID, actorID, userID, key string, values []string,
) (SubjectAttribute, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if !subjectAttributeKeyPattern.MatchString(key) {
		return SubjectAttribute{}, ErrSubjectAttributeInvalid
	}
	normalized, err := normalizeAttributeValues(values)
	if err != nil {
		return SubjectAttribute{}, err
	}
	var result SubjectAttribute
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := requireSubjectAttributeAdministratorTx(ctx, tx, actorID, userID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.subject_attributes(
				tenant_id,user_id,attribute_key,attribute_values,granted_by
			) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid)
			ON CONFLICT(tenant_id,user_id,attribute_key) DO UPDATE
			SET attribute_values=EXCLUDED.attribute_values,granted_by=EXCLUDED.granted_by
			RETURNING user_id::text,attribute_key,attribute_values,granted_by::text,updated_at::text`,
			tenantID, userID, key, normalized, actorID).Scan(
			&result.UserID, &result.Key, &result.Values, &result.GrantedBy, &result.UpdatedAt,
		); err != nil {
			return err
		}
		// The values themselves are business identifiers, not secrets, but the
		// audit records only the key and the count: an audit log is not the place
		// to accumulate a copy of everyone's data scope.
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'SET_SUBJECT_ATTRIBUTE','USER',$3,
				jsonb_build_object('attributeKey',$4::text,'valueCount',$5::int))`,
			tenantID, actorID, userID, key, len(normalized),
		)
		return err
	})
	return result, err
}

// DeleteSubjectAttribute revokes one attribute.
//
// Revoking is not a widening: every policy that references the attribute now
// denies this reader every row, which is the fail-closed direction.
func (s *AdminStore) DeleteSubjectAttribute(
	ctx context.Context, tenantID, actorID, userID, key string,
) error {
	key = strings.ToLower(strings.TrimSpace(key))
	if !subjectAttributeKeyPattern.MatchString(key) {
		return ErrSubjectAttributeInvalid
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := requireSubjectAttributeAdministratorTx(ctx, tx, actorID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.subject_attributes
			WHERE tenant_id=$1::uuid AND user_id=$2::uuid AND attribute_key=$3`,
			tenantID, userID, key); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'REVOKE_SUBJECT_ATTRIBUTE','USER',$3,
				jsonb_build_object('attributeKey',$4::text))`,
			tenantID, actorID, userID, key,
		)
		return err
	})
}
