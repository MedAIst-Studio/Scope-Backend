package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/MedAIst-Studio/MedAIist-Backend/domain"
	"github.com/google/uuid"
)

func (r *Repository) SaveFileInformation(ctx context.Context, file *domain.File) error {
	query := `INSERT INTO files (id, user_id, title, size, path, created_at, file_type, is_saved, source_url, extracted_text, thumbnail_url) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	_, err := r.db.ExecContext(ctx, query, file.Id, file.UserId, file.Name, file.Size, file.Path, file.CreatedAt, file.FileType, file.IsSaved, file.SourceURL, file.ExtractedText, file.ThumbnailUrl)
	if err != nil {
		return domain.ErrInternalServer
	}
	return nil
}

func (r *Repository) GetFilesByUserID(ctx context.Context, userId uuid.UUID) ([]domain.FileResponseDTO, error) {
	query := `SELECT id, title, size, path, created_at, file_type, is_saved, source_url, extracted_text, thumbnail_url FROM files WHERE user_id = $1`
	rows, err := r.db.QueryContext(ctx, query, userId)
	if err != nil {
		return nil, domain.ErrInternalServer
	}
	defer rows.Close()

	var files []domain.FileResponseDTO
	for rows.Next() {
		var f domain.FileResponseDTO
		var path string
		if err := rows.Scan(&f.Id, &f.Title, &f.Size, &path, &f.CreatedAt, &f.FileType, &f.IsSaved, &f.SourceURL, &f.ExtractedText, &f.ThumbnailUrl); err != nil {
			return nil, domain.ErrInternalServer
		}
		f.Url = path
		files = append(files, f)
	}
	return files, nil
}

func (r *Repository) DeleteFileById(ctx context.Context, id uuid.UUID) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM files WHERE id = $1", id)
	if err != nil {
		return err
	}

	if rowsAffected, _ := result.RowsAffected(); rowsAffected == 0 {
		return domain.ErrFileNotFound
	}

	return nil
}

func (r *Repository) DoesUserHaveFile(ctx context.Context, userId uuid.UUID, fileId uuid.UUID) (bool, error) {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM files WHERE id = $1 AND user_id = $2)`
	err := r.db.QueryRowContext(ctx, query, fileId, userId).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (r *Repository) GetFileMetadata(ctx context.Context, fileId uuid.UUID) (*domain.File, error) {
	query := `SELECT id, user_id, title, size, path, created_at, file_type, is_saved, source_url, extracted_text, thumbnail_url FROM files WHERE id = $1`
	var f domain.File
	err := r.db.QueryRowContext(ctx, query, fileId).Scan(&f.Id, &f.UserId, &f.Name, &f.Size, &f.Path, &f.CreatedAt, &f.FileType, &f.IsSaved, &f.SourceURL, &f.ExtractedText, &f.ThumbnailUrl)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrFileNotFound
		}
		return nil, domain.ErrInternalServer
	}
	return &f, nil
}

// ToggleFileSaved toggles the is_saved flag for a file and returns the new value.
func (r *Repository) ToggleFileSaved(ctx context.Context, fileId uuid.UUID) (bool, error) {
	query := `UPDATE files SET is_saved = NOT is_saved WHERE id = $1 RETURNING is_saved`
	var isSaved bool
	err := r.db.QueryRowContext(ctx, query, fileId).Scan(&isSaved)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, domain.ErrFileNotFound
		}
		return false, domain.ErrInternalServer
	}
	return isSaved, nil
}

// GetExpiredUnsavedImages returns image files that are unsaved and older than the given time.
func (r *Repository) GetExpiredUnsavedImages(ctx context.Context, olderThan time.Time) ([]domain.File, error) {
	query := `
		SELECT id, user_id, title, size, path, created_at, file_type, is_saved, source_url, extracted_text, thumbnail_url
		FROM files
		WHERE file_type = 'image' AND is_saved = FALSE AND created_at < $1
	`
	rows, err := r.db.QueryContext(ctx, query, olderThan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []domain.File
	for rows.Next() {
		var f domain.File
		if err := rows.Scan(&f.Id, &f.UserId, &f.Name, &f.Size, &f.Path, &f.CreatedAt, &f.FileType, &f.IsSaved, &f.SourceURL, &f.ExtractedText, &f.ThumbnailUrl); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// UpdateExtractedText updates the extracted_text field of a file.
func (r *Repository) UpdateExtractedText(ctx context.Context, fileId uuid.UUID, text string) error {
	query := `UPDATE files SET extracted_text = $1 WHERE id = $2`
	result, err := r.db.ExecContext(ctx, query, text, fileId)
	if err != nil {
		return domain.ErrInternalServer
	}

	if rowsAffected, _ := result.RowsAffected(); rowsAffected == 0 {
		return domain.ErrFileNotFound
	}

	return nil
}

// DeleteFilesByIDs deletes multiple files by their IDs in a single query.
func (r *Repository) DeleteFilesByIDs(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := fmt.Sprintf("DELETE FROM files WHERE id IN (%s)", strings.Join(placeholders, ","))
	_, err := r.db.ExecContext(ctx, query, args...)
	return err
}

// GetUserPDFCount returns the total number of PDF files a user has.
func (r *Repository) GetUserPDFCount(ctx context.Context, userID uuid.UUID) (int, error) {
	query := `SELECT COUNT(*) FROM files WHERE user_id = $1 AND file_type = 'pdf'`

	var count int
	err := r.db.QueryRowContext(ctx, query, userID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}
