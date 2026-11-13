package httpHandler

import (
	"net/http"
	"strconv"

	"github.com/TheRealHaui/Safety-Automation/internal/applicationModelStructs"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func pageReqFromQuery(r *http.Request) applicationModelStructs.PageRequest {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 {
		pageSize = 20
	}
	return applicationModelStructs.PageRequest{
		Page:     page,
		PageSize: pageSize,
		Search:   r.URL.Query().Get("search"),
		SortBy:   r.URL.Query().Get("sort_by"),
		SortDir:  r.URL.Query().Get("sort_dir"),
	}
}
