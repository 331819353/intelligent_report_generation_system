package savedquestion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (repository *PostgresRepository) Create(ctx context.Context, identity Identity, input CreateInput) (SavedQuestion, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || input.Validate() != nil ||
		input.SemanticIR.DomainID != identity.DomainID {
		return SavedQuestion{}, ErrInvalid
	}
	_, canonical, hash, err := ircontract.Canonicalize(input.SemanticIR)
	if err != nil {
		return SavedQuestion{}, ErrInvalid
	}
	id := askdata.ID(uuid.NewString())
	var result SavedQuestion
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO askdata.saved_questions(
			id,tenant_id,domain_id,owner_user_id,visibility,name,question_text,
			semantic_ir_json,semantic_ir_hash,semantic_release_id,semantic_release_content_hash,
			source_question_run_id
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING `+savedQuestionColumns, id, identity.TenantID, identity.DomainID, identity.ActorID,
			input.Visibility, strings.TrimSpace(input.Name), strings.TrimSpace(input.QuestionText), canonical,
			hash, input.SemanticIR.SemanticReleaseID, input.SemanticIR.SemanticContentHash,
			nullableID(input.SourceQuestionRunID))
		created, scanErr := scanSavedQuestion(row)
		if scanErr != nil {
			return scanErr
		}
		for _, dependency := range Dependencies(created.SemanticIR) {
			if _, insertErr := tx.Exec(ctx, `INSERT INTO askdata.saved_question_dependencies(
				tenant_id,saved_question_id,dependency_type,dependency_id
			) VALUES($1,$2,$3,$4)`, identity.TenantID, id, dependency.Type, dependency.ID); insertErr != nil {
				return insertErr
			}
		}
		result = created
		return nil
	})
	return result, err
}

func (repository *PostgresRepository) List(ctx context.Context, identity Identity) ([]SavedQuestion, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil {
		return nil, ErrInvalid
	}
	result := []SavedQuestion{}
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+savedQuestionColumns+` FROM askdata.saved_questions
			WHERE tenant_id=$1 AND domain_id=$2 AND status<>'ARCHIVED'
			ORDER BY updated_at DESC,id`, identity.TenantID, identity.DomainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, scanErr := scanSavedQuestion(rows)
			if scanErr != nil {
				return scanErr
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

type listCursor struct {
	SortValue string `json:"sortValue"`
	ID        string `json:"id"`
	Order     string `json:"order"`
}

func (repository *PostgresRepository) ListPage(ctx context.Context, identity Identity, limit int, cursor, order string) (Page, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || limit < 1 || limit > 200 {
		return Page{}, ErrInvalid
	}
	order = strings.ToUpper(strings.TrimSpace(order))
	if order == "" {
		order = "UPDATED_DESC"
	}
	if order != "UPDATED_DESC" && order != "CREATED_DESC" && order != "NAME_ASC" {
		return Page{}, ErrInvalid
	}
	var position listCursor
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &position) != nil || position.Order != order || askdata.ID(position.ID).Validate() != nil || position.SortValue == "" {
			return Page{}, ErrInvalid
		}
		if order != "NAME_ASC" {
			if _, err = time.Parse(time.RFC3339Nano, position.SortValue); err != nil {
				return Page{}, ErrInvalid
			}
		}
	}
	query := `SELECT ` + savedQuestionColumns + ` FROM askdata.saved_questions WHERE tenant_id=$1 AND domain_id=$2 AND status<>'ARCHIVED'`
	args := []any{identity.TenantID, identity.DomainID}
	if cursor != "" {
		if order == "NAME_ASC" {
			query += ` AND (lower(name),id)>(lower($3),$4::uuid)`
		} else if order == "CREATED_DESC" {
			query += ` AND (created_at,id)<($3::timestamptz,$4::uuid)`
		} else {
			query += ` AND (updated_at,id)<($3::timestamptz,$4::uuid)`
		}
		args = append(args, position.SortValue, position.ID)
	}
	if order == "NAME_ASC" {
		query += ` ORDER BY lower(name),id`
	} else if order == "CREATED_DESC" {
		query += ` ORDER BY created_at DESC,id DESC`
	} else {
		query += ` ORDER BY updated_at DESC,id DESC`
	}
	args = append(args, limit+1)
	query += ` LIMIT $` + fmt.Sprint(len(args))
	result := Page{Items: []SavedQuestion{}}
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, scanErr := scanSavedQuestion(rows)
			if scanErr != nil {
				return scanErr
			}
			result.Items = append(result.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return Page{}, err
	}
	if len(result.Items) > limit {
		last := result.Items[limit-1]
		value := last.UpdatedAt.Format(time.RFC3339Nano)
		if order == "CREATED_DESC" {
			value = last.CreatedAt.Format(time.RFC3339Nano)
		} else if order == "NAME_ASC" {
			value = last.Name
		}
		raw, _ := json.Marshal(listCursor{SortValue: value, ID: string(last.ID), Order: order})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		result.Items = result.Items[:limit]
	}
	return result, nil
}

func (repository *PostgresRepository) Get(ctx context.Context, identity Identity, id askdata.ID) (SavedQuestion, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || id.Validate() != nil {
		return SavedQuestion{}, ErrInvalid
	}
	var result SavedQuestion
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		item, scanErr := scanSavedQuestion(tx.QueryRow(ctx, `SELECT `+savedQuestionColumns+`
			FROM askdata.saved_questions WHERE tenant_id=$1 AND domain_id=$2 AND id=$3`,
			identity.TenantID, identity.DomainID, id))
		result = item
		return scanErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedQuestion{}, ErrNotFound
	}
	return result, err
}

func (repository *PostgresRepository) Share(ctx context.Context, identity Identity, id askdata.ID, input ShareInput) error {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || id.Validate() != nil || input.Validate() != nil {
		return ErrInvalid
	}
	return database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `INSERT INTO askdata.saved_question_shares(
			saved_question_id,tenant_id,principal_type,principal_id,granted_by
		) SELECT id,tenant_id,$4,$5,$3 FROM askdata.saved_questions
		WHERE id=$1 AND tenant_id=$2 AND owner_user_id=$3 AND status<>'ARCHIVED'
		ON CONFLICT(saved_question_id,principal_type,principal_id) DO NOTHING`,
			id, identity.TenantID, identity.ActorID, input.PrincipalType, input.PrincipalID)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			return ErrPermissionDenied
		}
		return nil
	})
}

func (repository *PostgresRepository) Promote(ctx context.Context, identity Identity, id askdata.ID) error {
	return repository.ownerUpdate(ctx, identity, id, `visibility='CERTIFIED_CANDIDATE'`)
}

func (repository *PostgresRepository) Archive(ctx context.Context, identity Identity, id askdata.ID) error {
	return repository.ownerUpdate(ctx, identity, id, `status='ARCHIVED',migration_reason=NULL`)
}

func (repository *PostgresRepository) MarkNeedsMigration(ctx context.Context, tenantID askdata.ID, kind, objectID, reason string) (int64, error) {
	if repository == nil || repository.pool == nil || tenantID.Validate() != nil || strings.TrimSpace(reason) == "" {
		return 0, ErrInvalid
	}
	var count int64
	err := database.WithTenantTx(ctx, repository.pool, string(tenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE askdata.saved_questions AS question SET
			status='NEEDS_MIGRATION',migration_reason=$4
			FROM askdata.saved_question_dependencies AS dependency
			WHERE dependency.tenant_id=$1 AND dependency.dependency_type=$2 AND dependency.dependency_id=$3
			  AND question.id=dependency.saved_question_id AND question.tenant_id=dependency.tenant_id
			  AND question.status='ACTIVE'`, tenantID, kind, objectID, strings.TrimSpace(reason))
		if err == nil {
			count = command.RowsAffected()
		}
		return err
	})
	return count, err
}

func (repository *PostgresRepository) ownerUpdate(ctx context.Context, identity Identity, id askdata.ID, assignment string) error {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || id.Validate() != nil {
		return ErrInvalid
	}
	return database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE askdata.saved_questions SET %s
			WHERE id=$1 AND tenant_id=$2 AND domain_id=$3 AND owner_user_id=$4 AND status<>'ARCHIVED'`, assignment),
			id, identity.TenantID, identity.DomainID, identity.ActorID)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const savedQuestionColumns = `id::text,tenant_id::text,domain_id::text,owner_user_id::text,
	visibility,name,question_text,semantic_ir_json,semantic_ir_hash,semantic_release_id::text,
	semantic_release_content_hash,COALESCE(source_question_run_id::text,''),status,
	COALESCE(migration_reason,''),created_at,updated_at`

type rowScanner interface{ Scan(...any) error }

func scanSavedQuestion(row rowScanner) (SavedQuestion, error) {
	var item SavedQuestion
	var raw []byte
	if err := row.Scan(&item.ID, &item.TenantID, &item.DomainID, &item.OwnerUserID, &item.Visibility,
		&item.Name, &item.QuestionText, &raw, &item.SemanticIRHash, &item.SemanticReleaseID,
		&item.SemanticReleaseContentHash, &item.SourceQuestionRunID, &item.Status, &item.MigrationReason,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return SavedQuestion{}, err
	}
	ir, err := ircontract.Decode(raw)
	if err != nil {
		return SavedQuestion{}, err
	}
	item.SemanticIR = ir
	return item, nil
}

func nullableID(id askdata.ID) any {
	if id == "" {
		return nil
	}
	return id
}
