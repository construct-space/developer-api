package handlers

import (
	"net/http"
	"strconv"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// GET /api/admin/spaces/all
// Lists all spaces regardless of status. Supports status, publisher_id, q filters and pagination.
// Gated by X-Internal-Secret so oracle can proxy it.
func AdminListAllSpaces(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	status := r.URL.Query().Get("status")
	publisherID := r.URL.Query().Get("publisher_id")
	q := r.URL.Query().Get("q")
	pageParam := r.URL.Query().Get("page")
	limitParam := r.URL.Query().Get("limit")

	page := 1
	limit := 50
	if v, err := strconv.Atoi(pageParam); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(limitParam); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	offset := (page - 1) * limit

	db := database.DB.Model(&models.Space{})
	if status != "" {
		db = db.Where("status = ?", status)
	}
	if publisherID != "" {
		var pub models.Publisher
		if err := database.DB.First(&pub, publisherID).Error; err == nil {
			db = db.Where("publisher_name = ?", pub.Name)
		}
	}
	if q != "" {
		like := "%" + q + "%"
		db = db.Where("name LIKE ? OR display_name LIKE ? OR author LIKE ?", like, like, like)
	}

	var total int64
	db.Count(&total)

	var spaces []models.Space
	db.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&spaces)

	items := make([]map[string]any, 0, len(spaces))
	for _, s := range spaces {
		item := s.ToJSON()

		if s.PublisherName != nil && *s.PublisherName != "" {
			var pub models.Publisher
			if err := database.DB.Where("name = ?", *s.PublisherName).First(&pub).Error; err == nil {
				item["publisher_info"] = map[string]any{
					"id":       pub.ID,
					"name":     pub.Name,
					"email":    pub.Email,
					"kind":     pub.Kind(),
					"verified": pub.Verified,
				}
			}
		}

		items = append(items, item)
	}

	WriteJSON(w, 200, map[string]any{
		"spaces": items,
		"total":  total,
		"page":   page,
		"limit":  limit,
	})
}
