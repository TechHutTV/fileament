package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

type cardFile struct {
	Format        string `json:"format"`
	TriangleCount int    `json:"triangleCount"`
}

type modelSummary struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	PrimaryThumb string     `json:"primaryThumb,omitempty"`
	TotalBytes   int64      `json:"totalBytes"`
	CreatedAt    int64      `json:"createdAt"`
	UpdatedAt    int64      `json:"updatedAt"`
	Files        []cardFile `json:"files"`
	Position     int64      `json:"-"`
}

const cardColumns = `models.id,models.title,COALESCE(models.primary_thumb,''),models.total_bytes,models.created_at,models.updated_at,COALESCE(card_file.format,''),COALESCE(card_file.triangle_count,0)`
const cardJoin = ` LEFT JOIN files card_file ON card_file.id = (SELECT id FROM files WHERE model_id=models.id ORDER BY sort_order,filename,id LIMIT 1)`

func (a *App) queryModelSummaries(ctx context.Context, join, where, order, position string, args ...any) ([]modelSummary, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT `+cardColumns+`,`+position+` FROM models`+join+cardJoin+` WHERE `+where+` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []modelSummary{}
	for rows.Next() {
		m := modelSummary{Files: []cardFile{}}
		var file cardFile
		if err := rows.Scan(&m.ID, &m.Title, &m.PrimaryThumb, &m.TotalBytes, &m.CreatedAt, &m.UpdatedAt, &file.Format, &file.TriangleCount, &m.Position); err != nil {
			return nil, err
		}
		if file.Format != "" {
			m.Files = append(m.Files, file)
		}
		items = append(items, m)
	}
	return items, errors.Join(rows.Err(), rows.Close())
}

type collectionPage struct {
	Collection
	Models     []modelSummary `json:"models"`
	ModelCount int            `json:"modelCount"`
	NextCursor string         `json:"nextCursor"`
}

var errInvalidCursor = errors.New("invalid cursor")

func collectionReadStatus(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	if errors.Is(err, errInvalidCursor) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func (a *App) collectionMetadata(ctx context.Context, idOrSlug string) (Collection, error) {
	var c Collection
	err := a.db.QueryRowContext(ctx, `SELECT id,name,slug,description,COALESCE(cover_model_id,''),created_at FROM collections WHERE id = ? OR slug = ? ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END LIMIT 1`, idOrSlug, idOrSlug, idOrSlug).Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.CoverModelID, &c.CreatedAt)
	return c, err
}

func (a *App) getCollectionPage(ctx context.Context, idOrSlug string, limit int, cursor string) (collectionPage, error) {
	c, err := a.collectionMetadata(ctx, idOrSlug)
	page := collectionPage{Collection: c}
	if err != nil {
		return page, err
	}
	where := `cm.collection_id = ?`
	args := []any{c.ID}
	if cursor != "" {
		decoded, err := decodeCursor(cursor)
		if err != nil || decoded.Sort != "collection:"+c.ID {
			return page, errInvalidCursor
		}
		position, err := strconv.ParseInt(decoded.Value, 10, 64)
		if err != nil {
			return page, errInvalidCursor
		}
		where += ` AND (cm.sort_order > ? OR (cm.sort_order = ? AND cm.model_id > ?))`
		args = append(args, position, position, decoded.ID)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_models WHERE collection_id=?`, c.ID).Scan(&page.ModelCount); err != nil {
		return page, err
	}
	page.Models, err = a.queryModelSummaries(ctx, ` JOIN collection_models cm ON cm.model_id=models.id`, where, `cm.sort_order,cm.model_id`, `cm.sort_order`, append(args, limit+1)...)
	if err != nil {
		return page, err
	}
	if len(page.Models) > limit {
		page.Models = page.Models[:limit]
		last := page.Models[limit-1]
		page.NextCursor = encodeCursor("collection:"+c.ID, Model{ID: last.ID, Title: strconv.FormatInt(last.Position, 10)})
	}
	for _, model := range page.Models {
		page.ModelIDs = append(page.ModelIDs, model.ID)
	}
	return page, nil
}
