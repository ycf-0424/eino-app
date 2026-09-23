package attachments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MySQLRepository stores metadata/artifacts only; original bytes remain in app-data.
type MySQLRepository struct{ db *sql.DB }

func NewMySQLRepository(db *sql.DB) (*MySQLRepository, error) {
	if db == nil {
		return nil, errors.New("attachment repository requires a database")
	}
	return &MySQLRepository{db: db}, nil
}

func (r *MySQLRepository) CheckSchema(ctx context.Context) error {
	for _, table := range []string{"attachments", "attachment_artifacts"} {
		if _, err := r.db.ExecContext(ctx, "SELECT 1 FROM "+table+" LIMIT 0"); err != nil {
			return fmt.Errorf("attachment schema is unavailable (run make db-attachments-migrate): %w", err)
		}
	}
	return nil
}

func (r *MySQLRepository) Create(ctx context.Context, item Attachment) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO attachments
		(id,owner_id,original_name,sha256,mime_type,size_bytes,storage_key,status,error_reason,retained_until,parser_version,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.OwnerID, item.OriginalName, item.SHA256, item.MIMEType,
		item.SizeBytes, item.StorageKey, item.Status, item.ErrorReason, item.RetainedUntil, item.ParserVersion, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert attachment: %w", err)
	}
	return nil
}

func (r *MySQLRepository) BeginProcessing(ctx context.Context, owner, id string, expected Status) error {
	result, err := r.db.ExecContext(ctx, `UPDATE attachments SET status=?,error_reason='',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND owner_id=? AND status=?`,
		StatusProcessing, id, owner, expected)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrInvalidState
	}
	return nil
}

func (r *MySQLRepository) Get(ctx context.Context, owner, id string) (Attachment, error) {
	var item Attachment
	err := r.db.QueryRowContext(ctx, `SELECT id,owner_id,original_name,sha256,mime_type,size_bytes,storage_key,status,error_reason,retained_until,parser_version,created_at,updated_at
		FROM attachments WHERE id=? AND owner_id=?`, id, owner).Scan(&item.ID, &item.OwnerID, &item.OriginalName, &item.SHA256, &item.MIMEType,
		&item.SizeBytes, &item.StorageKey, &item.Status, &item.ErrorReason, &item.RetainedUntil, &item.ParserVersion, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("load attachment: %w", err)
	}
	item.RetainedUntil = item.RetainedUntil.UTC()
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item, nil
}

func (r *MySQLRepository) List(ctx context.Context, owner string, limit int) ([]Attachment, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,owner_id,original_name,sha256,mime_type,size_bytes,storage_key,status,error_reason,retained_until,parser_version,created_at,updated_at
		FROM attachments WHERE owner_id=? ORDER BY created_at DESC LIMIT ?`, owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Attachment, 0)
	for rows.Next() {
		var item Attachment
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.OriginalName, &item.SHA256, &item.MIMEType, &item.SizeBytes,
			&item.StorageKey, &item.Status, &item.ErrorReason, &item.RetainedUntil, &item.ParserVersion, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.RetainedUntil = item.RetainedUntil.UTC()
		item.CreatedAt = item.CreatedAt.UTC()
		item.UpdatedAt = item.UpdatedAt.UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *MySQLRepository) SetStatus(ctx context.Context, owner, id string, status Status, reason, parserVersion string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE attachments SET status=?,error_reason=?,parser_version=?,updated_at=UTC_TIMESTAMP(6) WHERE id=? AND owner_id=?`,
		status, reason, parserVersion, id, owner)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *MySQLRepository) SaveArtifacts(ctx context.Context, owner, id string, artifacts []Artifact) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM attachment_artifacts WHERE attachment_id=? AND owner_id=?`, id, owner); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		blocks, err := json.Marshal(artifact.Blocks)
		if err != nil {
			return err
		}
		var confidence any
		if artifact.OCRConfidence != nil {
			confidence = *artifact.OCRConfidence
		}
		var transcriptConfidence any
		if artifact.Confidence != nil {
			transcriptConfidence = *artifact.Confidence
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachment_artifacts
			(id,attachment_id,owner_id,source_ref,text,blocks,ocr_confidence,confidence,extractor_version,content_hash,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, artifact.ID, id, owner, artifact.SourceRef, artifact.Text, blocks, confidence, transcriptConfidence,
			artifact.ExtractorVersion, artifact.ContentHash, artifact.CreatedAt); err != nil {
			return fmt.Errorf("insert attachment artifact: %w", err)
		}
	}
	return tx.Commit()
}

func (r *MySQLRepository) Artifacts(ctx context.Context, owner, id string) ([]Artifact, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,attachment_id,owner_id,source_ref,text,blocks,ocr_confidence,confidence,extractor_version,content_hash,created_at
		FROM attachment_artifacts WHERE attachment_id=? AND owner_id=? ORDER BY id`, id, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Artifact, 0)
	for rows.Next() {
		var item Artifact
		var blocks []byte
		var confidence, transcriptConfidence sql.NullFloat64
		if err := rows.Scan(&item.ID, &item.AttachmentID, &item.OwnerID, &item.SourceRef, &item.Text, &blocks, &confidence, &transcriptConfidence,
			&item.ExtractorVersion, &item.ContentHash, &item.CreatedAt); err != nil {
			return nil, err
		}
		if len(blocks) > 0 {
			if err := json.Unmarshal(blocks, &item.Blocks); err != nil {
				return nil, fmt.Errorf("decode attachment artifact blocks: %w", err)
			}
		}
		if confidence.Valid {
			item.OCRConfidence = &confidence.Float64
		}
		if transcriptConfidence.Valid {
			item.Confidence = &transcriptConfidence.Float64
		}
		item.CreatedAt = item.CreatedAt.UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *MySQLRepository) Delete(ctx context.Context, owner, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM attachments WHERE id=? AND owner_id=?`, id, owner)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *MySQLRepository) Expired(ctx context.Context, now time.Time, limit int) ([]Attachment, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,owner_id,original_name,sha256,mime_type,size_bytes,storage_key,status,error_reason,retained_until,parser_version,created_at,updated_at
		FROM attachments WHERE retained_until<=? ORDER BY retained_until LIMIT ?`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Attachment, 0)
	for rows.Next() {
		var item Attachment
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.OriginalName, &item.SHA256, &item.MIMEType, &item.SizeBytes,
			&item.StorageKey, &item.Status, &item.ErrorReason, &item.RetainedUntil, &item.ParserVersion, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
