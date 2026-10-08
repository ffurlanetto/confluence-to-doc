package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
)

type handlers struct{ d Deps }

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "Invalid JSON request body.")
		return false
	}
	return true
}

// ------------------------------------------------------------------ me

type meResponse struct {
	ID      uuid.UUID `json:"id"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	IsAdmin bool      `json:"isAdmin"`
}

func (h *handlers) me(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	writeJSON(w, http.StatusOK, meResponse{ID: u.ID, Email: u.Email, Name: u.Name, IsAdmin: u.IsAdmin})
}

// --------------------------------------------------------- preferences

type preferencesResponse struct {
	ConfluenceBaseURL string     `json:"confluenceBaseUrl"`
	HasPAT            bool       `json:"hasPat"`
	PATUpdatedAt      *time.Time `json:"patUpdatedAt,omitempty"`
	DefaultFormat     string     `json:"defaultFormat"`
	RetentionHours    int        `json:"retentionHours"`
	MaxPages          int        `json:"maxPages"`
	DocumentTemplate  string     `json:"documentTemplate,omitempty"`
	// Classifications offered when exporting; the default applies when the
	// user picks none.
	Classifications       []classificationDTO `json:"classifications"`
	DefaultClassification string              `json:"defaultClassification,omitempty"`
}

type classificationDTO struct {
	Label     string `json:"label"`
	Watermark bool   `json:"watermark"`
}

func (h *handlers) getPreferences(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	p, err := h.d.Accounts.Preferences(r.Context(), u.ID)
	if err != nil {
		handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preferencesResponse{
		ConfluenceBaseURL:     h.d.Accounts.ConfluenceURL().String(),
		HasPAT:                p.HasPAT(),
		PATUpdatedAt:          p.PATUpdatedAt,
		DefaultFormat:         string(p.DefaultFormat),
		RetentionHours:        int(h.d.Retention.Hours()),
		MaxPages:              h.d.MaxPages,
		DocumentTemplate:      h.d.DocumentTemplate,
		Classifications:       classificationDTOs(h.d.Classifications),
		DefaultClassification: h.d.DefaultClassification,
	})
}

func (h *handlers) putPreferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DefaultFormat string `json:"defaultFormat"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	f, err := domain.ParseFormat(req.DefaultFormat)
	if err != nil {
		handleError(w, r, err)
		return
	}
	if err := h.d.Accounts.SetDefaultFormat(r.Context(), auth.UserFrom(r.Context()).ID, f); err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionPreferences, Details: map[string]any{"defaultFormat": string(f)}})
	h.getPreferences(w, r)
}

func (h *handlers) putPAT(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cu, err := h.d.Accounts.SetPAT(r.Context(), auth.UserFrom(r.Context()).ID, req.Token)
	if err != nil {
		// Never the token itself: only that a rejected one was submitted.
		h.record(r, domain.AuditEvent{Action: audit.ActionPATSet, Outcome: domain.AuditFailure,
			Details: map[string]any{"reason": errorReason(err)}})
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionPATSet, Details: map[string]any{"confluenceUser": cu.Username}})
	writeJSON(w, http.StatusOK, map[string]string{"confluenceUser": cu.DisplayName})
}

func (h *handlers) deletePAT(w http.ResponseWriter, r *http.Request) {
	if err := h.d.Accounts.ClearPAT(r.Context(), auth.UserFrom(r.Context()).ID); err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionPATDelete})
	w.WriteHeader(http.StatusNoContent)
}

func classificationDTOs(levels []domain.Classification) []classificationDTO {
	out := make([]classificationDTO, 0, len(levels))
	for _, c := range levels {
		out = append(out, classificationDTO{Label: c.Label, Watermark: c.Watermark})
	}
	return out
}

// classification returns the configured label matching the requested one
// (case-insensitively), "" when none was requested.
func (h *handlers) classification(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", nil
	}
	for _, c := range h.d.Classifications {
		if strings.EqualFold(c.Label, requested) {
			return c.Label, nil
		}
	}
	return "", domain.ErrInvalidClassification
}

// ---------------------------------------------------------- confluence

func (h *handlers) client(w http.ResponseWriter, r *http.Request) *confluence.Client {
	c, err := h.d.Accounts.Client(r.Context(), auth.UserFrom(r.Context()).ID)
	if err != nil {
		handleError(w, r, err)
		return nil
	}
	return c
}

// searchPages accepts a free-text query, a page id or a Confluence page URL.
func (h *handlers) searchPages(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 500 {
		writeJSON(w, http.StatusOK, map[string]any{"results": []confluence.PageSummary{}})
		return
	}
	c := h.client(w, r)
	if c == nil {
		return
	}
	var results []confluence.PageSummary
	if ref, ok := confluence.ParsePageRef(q); ok {
		var p *confluence.Page
		var err error
		if ref.ID != "" {
			p, err = c.GetPage(r.Context(), ref.ID, false)
		} else {
			p, err = c.FindPageByTitle(r.Context(), ref.SpaceKey, ref.Title)
		}
		if err != nil && !errors.Is(err, confluence.ErrNotFound) {
			handleError(w, r, err)
			return
		}
		if p != nil {
			results = append(results, p.PageSummary)
		}
	} else {
		var err error
		results, err = c.SearchPages(r.Context(), q, r.URL.Query().Get("space"), 25)
		if err != nil {
			handleError(w, r, err)
			return
		}
	}
	if results == nil {
		results = []confluence.PageSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (h *handlers) getPage(w http.ResponseWriter, r *http.Request) {
	c := h.client(w, r)
	if c == nil {
		return
	}
	p, err := c.GetPage(r.Context(), chi.URLParam(r, "pageID"), false)
	if err != nil {
		handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p.PageSummary)
}

func (h *handlers) getChildren(w http.ResponseWriter, r *http.Request) {
	c := h.client(w, r)
	if c == nil {
		return
	}
	pages, err := c.ListChildren(r.Context(), chi.URLParam(r, "pageID"), false)
	if err != nil {
		handleError(w, r, err)
		return
	}
	out := make([]confluence.PageSummary, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.PageSummary)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// ------------------------------------------------------------- exports

type exportDTO struct {
	ID              uuid.UUID  `json:"id"`
	PageID          string     `json:"pageId"`
	Title           string     `json:"title"`
	Format          string     `json:"format"`
	IncludeChildren bool       `json:"includeChildren"`
	Classification  string     `json:"classification,omitempty"`
	Status          string     `json:"status"`
	Error           string     `json:"error,omitempty"`
	Attempts        int        `json:"attempts"`
	PagesDone       int        `json:"pagesDone"`
	PagesTotal      int        `json:"pagesTotal"`
	FileSize        int64      `json:"fileSize,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
}

func toDTO(e *domain.Export) exportDTO {
	return exportDTO{
		ID: e.ID, PageID: e.RootPageID, Title: e.RootTitle, Format: string(e.Format),
		IncludeChildren: e.IncludeChildren, Classification: e.Classification, Status: string(e.Status), Error: e.Error, Attempts: e.Attempts,
		PagesDone: e.PagesDone, PagesTotal: e.PagesTotal, FileSize: e.FileSize,
		CreatedAt: e.CreatedAt, StartedAt: e.StartedAt, FinishedAt: e.FinishedAt, ExpiresAt: e.ExpiresAt,
	}
}

func (h *handlers) listExports(w http.ResponseWriter, r *http.Request) {
	list, err := h.d.Exports.List(r.Context(), auth.UserFrom(r.Context()).ID)
	if err != nil {
		handleError(w, r, err)
		return
	}
	out := make([]exportDTO, 0, len(list))
	for _, e := range list {
		out = append(out, toDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"exports": out})
}

func (h *handlers) createExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PageID          string `json:"pageId"`
		Format          string `json:"format"`
		IncludeChildren *bool  `json:"includeChildren"`
		Classification  string `json:"classification"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	format, err := domain.ParseFormat(req.Format)
	if err != nil {
		handleError(w, r, err)
		return
	}
	ref, ok := confluence.ParsePageRef(req.PageID)
	if !ok || ref.ID == "" {
		handleError(w, r, domain.ErrInvalidPageID)
		return
	}
	includeChildren := req.IncludeChildren == nil || *req.IncludeChildren
	classification, err := h.classification(req.Classification)
	if err != nil {
		handleError(w, r, err)
		return
	}

	// Fail fast (synchronously) on an invalid PAT or an inaccessible page,
	// and capture the title for the export list.
	c := h.client(w, r)
	if c == nil {
		return
	}
	page, err := c.GetPage(r.Context(), ref.ID, false)
	if err != nil {
		handleError(w, r, err)
		return
	}
	e, err := h.d.Exports.Create(r.Context(), export.CreateRequest{
		UserID: auth.UserFrom(r.Context()).ID, RootPageID: page.ID, RootTitle: page.Title,
		Format: format, IncludeChildren: includeChildren, Classification: classification,
	})
	if err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{
		Action: audit.ActionExportCreate, TargetType: audit.TargetExport, TargetID: e.ID.String(),
		Details: map[string]any{"pageId": page.ID, "title": page.Title, "space": page.SpaceKey,
			"format": string(format), "includeChildren": includeChildren, "classification": classification},
	})
	w.Header().Set("Location", "/api/exports/"+e.ID.String())
	writeJSON(w, http.StatusAccepted, toDTO(e))
}

func exportID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "exportID"))
	if err != nil {
		handleError(w, r, domain.ErrNotFound)
		return uuid.Nil, false
	}
	return id, true
}

func (h *handlers) getExport(w http.ResponseWriter, r *http.Request) {
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	e, err := h.d.Exports.Get(r.Context(), id, auth.UserFrom(r.Context()).ID)
	if err != nil {
		handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(e))
}

func (h *handlers) deleteExport(w http.ResponseWriter, r *http.Request) {
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	if err := h.d.Exports.Delete(r.Context(), id, auth.UserFrom(r.Context()).ID); err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionExportDelete, TargetType: audit.TargetExport, TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) download(w http.ResponseWriter, r *http.Request) {
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	e, f, err := h.d.Exports.Open(r.Context(), id, auth.UserFrom(r.Context()).ID)
	if err != nil {
		handleError(w, r, err)
		return
	}
	defer f.Close()
	name := fileName(e.RootTitle, e.Format)
	// A resumed download arrives as several range requests: record the one
	// that starts the file, not every chunk.
	if rg := r.Header.Get("Range"); rg == "" || strings.HasPrefix(rg, "bytes=0-") {
		h.record(r, domain.AuditEvent{
			Action: audit.ActionExportDownload, TargetType: audit.TargetExport, TargetID: e.ID.String(),
			Details: map[string]any{"pageId": e.RootPageID, "title": e.RootTitle, "format": string(e.Format),
				"size": e.FileSize, "pages": e.PagesTotal, "classification": e.Classification},
		})
	}
	w.Header().Set("Content-Type", e.Format.ContentType())
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	modTime := e.CreatedAt
	if e.FinishedAt != nil {
		modTime = *e.FinishedAt
	}
	http.ServeContent(w, r, name, modTime, f)
}

// fileName derives a safe download file name from the page title.
func fileName(title string, f domain.Format) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('_')
		}
		if b.Len() >= 100 {
			break
		}
	}
	name := strings.Trim(b.String(), "._")
	if name == "" {
		name = "export"
	}
	return fmt.Sprintf("%s.%s", name, f.Extension())
}
