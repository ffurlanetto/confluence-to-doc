package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/admin"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/doctemplate"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// ------------------------------------------------------------ admin: queue

type adminExportDTO struct {
	exportDTO
	OwnerID    uuid.UUID `json:"ownerId"`
	OwnerEmail string    `json:"ownerEmail,omitempty"`
}

var queueStatuses = map[string]domain.ExportStatus{
	"queued": domain.StatusQueued, "running": domain.StatusRunning, "succeeded": domain.StatusSucceeded,
	"failed": domain.StatusFailed, "expired": domain.StatusExpired,
}

// listQueue lists exports of every user; ?status=queued,running narrows it.
func (h *handlers) listQueue(w http.ResponseWriter, r *http.Request) {
	var statuses []domain.ExportStatus
	if v := r.URL.Query().Get("status"); v != "" {
		for _, name := range strings.Split(v, ",") {
			st, ok := queueStatuses[strings.TrimSpace(name)]
			if !ok {
				writeError(w, r, http.StatusBadRequest, "invalid_filter", "Unknown export status.")
				return
			}
			statuses = append(statuses, st)
		}
	}
	list, err := h.d.Admin.Queue(r.Context(), statuses)
	if err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionAdminQueueRead, Details: map[string]any{"status": r.URL.Query().Get("status")}})
	out := make([]adminExportDTO, 0, len(list))
	for i := range list {
		out = append(out, adminExportDTO{exportDTO: toDTO(&list[i].Export), OwnerID: list[i].UserID, OwnerEmail: list[i].OwnerEmail})
	}
	writeJSON(w, http.StatusOK, map[string]any{"exports": out, "limit": admin.QueueLimit})
}

func (h *handlers) cancelExport(w http.ResponseWriter, r *http.Request) {
	h.exportAction(w, r, audit.ActionAdminExportCancel, h.d.Admin.Cancel)
}

func (h *handlers) retryExport(w http.ResponseWriter, r *http.Request) {
	h.exportAction(w, r, audit.ActionAdminExportRetry, h.d.Admin.Retry)
}

func (h *handlers) exportAction(w http.ResponseWriter, r *http.Request, action string,
	do func(ctx context.Context, id uuid.UUID) (*domain.Export, error),
) {
	id, err := uuid.Parse(chi.URLParam(r, "exportID"))
	if err != nil {
		handleError(w, r, domain.ErrNotFound)
		return
	}
	e, err := do(r.Context(), id)
	event := domain.AuditEvent{Action: action, TargetType: audit.TargetExport, TargetID: id.String()}
	if err != nil {
		if errors.Is(err, domain.ErrExportState) {
			event.Outcome, event.Details = domain.AuditFailure, map[string]any{"reason": "invalid_state"}
			h.record(r, event)
		}
		handleError(w, r, err)
		return
	}
	event.Details = map[string]any{"owner": e.UserID.String(), "pageId": e.RootPageID, "format": string(e.Format)}
	h.record(r, event)
	writeJSON(w, http.StatusOK, toDTO(e))
}

// ------------------------------------------------------------ admin: users

type adminUserDTO struct {
	ID            uuid.UUID  `json:"id"`
	Email         string     `json:"email"`
	Name          string     `json:"name"`
	IsAdmin       bool       `json:"isAdmin"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastActiveAt  time.Time  `json:"lastActiveAt"`
	BlockedAt     *time.Time `json:"blockedAt,omitempty"`
	BlockedReason string     `json:"blockedReason,omitempty"`
	ActiveExports int        `json:"activeExports"`
	TotalExports  int        `json:"totalExports"`
}

func (h *handlers) listUsers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", "Filter value too long.")
		return
	}
	list, err := h.d.Admin.Users(r.Context(), q)
	if err != nil {
		handleError(w, r, err)
		return
	}
	details := map[string]any{}
	if q != "" {
		details["q"] = q
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionAdminUsersRead, Details: details})
	out := make([]adminUserDTO, 0, len(list))
	for _, u := range list {
		out = append(out, adminUserDTO{
			ID: u.ID, Email: u.Email, Name: u.Name, IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt, LastActiveAt: u.LastActiveAt,
			BlockedAt: u.BlockedAt, BlockedReason: u.BlockedReason, ActiveExports: u.ActiveExports, TotalExports: u.TotalExports,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "limit": admin.UserLimit})
}

func (h *handlers) blockUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		handleError(w, r, domain.ErrNotFound)
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || len(req.Reason) > admin.MaxReasonLength {
		writeError(w, r, http.StatusBadRequest, "invalid_reason", "Give a reason of at most 500 characters.")
		return
	}
	u, cancelled, err := h.d.Admin.Block(r.Context(), auth.UserFrom(r.Context()), id, req.Reason)
	if err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{
		Action: audit.ActionAdminUserBlock, TargetType: audit.TargetUser, TargetID: id.String(),
		Details: map[string]any{"email": u.Email, "reason": req.Reason, "cancelledExports": cancelled},
	})
	writeJSON(w, http.StatusOK, map[string]any{"blockedAt": u.BlockedAt, "cancelledExports": cancelled})
}

func (h *handlers) unblockUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		handleError(w, r, domain.ErrNotFound)
		return
	}
	u, err := h.d.Admin.Unblock(r.Context(), id)
	if err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{
		Action: audit.ActionAdminUserUnblock, TargetType: audit.TargetUser, TargetID: id.String(),
		Details: map[string]any{"email": u.Email},
	})
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------ admin: usage

type usageDTO struct {
	From      time.Time      `json:"from"`
	Succeeded int            `json:"succeeded"`
	Failed    int            `json:"failed"`
	Pages     int64          `json:"pages"`
	Bytes     int64          `json:"bytes"`
	Users     int            `json:"users"`
	ByFormat  map[string]int `json:"byFormat"`
	Daily     []dailyDTO     `json:"daily"`
	TopUsers  []topUserDTO   `json:"topUsers"`
}

type dailyDTO struct {
	Day       string `json:"day"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

type topUserDTO struct {
	UserID  uuid.UUID `json:"userId"`
	Email   string    `json:"email"`
	Exports int       `json:"exports"`
	Pages   int64     `json:"pages"`
}

func (h *handlers) usage(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > admin.MaxUsageDays {
			writeError(w, r, http.StatusBadRequest, "invalid_filter", "The period must be between 1 and 366 days.")
			return
		}
		days = n
	}
	u, err := h.d.Admin.Usage(r.Context(), days)
	if err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionAdminUsageRead, Details: map[string]any{"days": days}})
	out := usageDTO{
		From: u.From, Succeeded: u.Succeeded, Failed: u.Failed, Pages: u.Pages, Bytes: u.Bytes, Users: u.Users,
		ByFormat: map[string]int{}, Daily: make([]dailyDTO, 0, len(u.Daily)), TopUsers: make([]topUserDTO, 0, len(u.TopUsers)),
	}
	for f, n := range u.ByFormat {
		out.ByFormat[string(f)] = n
	}
	for _, d := range u.Daily {
		out.Daily = append(out.Daily, dailyDTO{Day: d.Day.Format(time.DateOnly), Succeeded: d.Succeeded, Failed: d.Failed})
	}
	for _, t := range u.TopUsers {
		out.TopUsers = append(out.TopUsers, topUserDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// --------------------------------------------------------- admin: template

type templateDTO struct {
	Origin                string     `json:"origin"`
	Name                  string     `json:"name,omitempty"`
	DefaultParagraphStyle string     `json:"defaultParagraphStyle,omitempty"`
	Styles                int        `json:"styles"`
	UploadedAt            *time.Time `json:"uploadedAt,omitempty"`
	UploadedBy            string     `json:"uploadedBy,omitempty"`
	Configured            string     `json:"configured,omitempty"`
	MaxBytes              int        `json:"maxBytes"`
}

func (h *handlers) getTemplate(w http.ResponseWriter, r *http.Request) {
	info, err := h.d.Templates.Info(r.Context())
	if err != nil {
		handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTemplateDTO(info))
}

// putTemplate takes the file as the raw request body, its name in ?name=.
func (h *handlers) putTemplate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" || len(name) > 255 {
		writeError(w, r, http.StatusBadRequest, "invalid_template", "Give the template's file name.")
		return
	}
	content, err := io.ReadAll(r.Body)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		err = docx.ErrInvalidTemplate
	}
	if err == nil {
		var info doctemplate.Info
		if info, err = h.d.Templates.Upload(r.Context(), name, content, auth.UserFrom(r.Context()).ID); err == nil {
			h.record(r, domain.AuditEvent{
				Action:  audit.ActionAdminTemplateUpload,
				Details: map[string]any{"name": info.Name, "bytes": len(content)},
			})
			writeJSON(w, http.StatusOK, toTemplateDTO(info))
			return
		}
	}
	if errors.Is(err, docx.ErrInvalidTemplate) || errors.Is(err, docx.ErrUnsafeTemplate) {
		slog.WarnContext(r.Context(), "Word template refused", "err", err)
		reason := "invalid"
		if errors.Is(err, docx.ErrUnsafeTemplate) {
			reason = "unsafe"
		}
		h.record(r, domain.AuditEvent{
			Action: audit.ActionAdminTemplateUpload, Outcome: domain.AuditFailure,
			Details: map[string]any{"name": name, "reason": reason},
		})
	}
	handleError(w, r, err)
}

func (h *handlers) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := h.d.Templates.Reset(r.Context()); err != nil {
		handleError(w, r, err)
		return
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionAdminTemplateReset})
	w.WriteHeader(http.StatusNoContent)
}

func toTemplateDTO(i doctemplate.Info) templateDTO {
	return templateDTO{
		Origin: i.Origin, Name: i.Name, DefaultParagraphStyle: i.DefaultParagraphStyle, Styles: i.Styles,
		UploadedAt: i.UploadedAt, UploadedBy: i.UploadedBy, Configured: i.Configured, MaxBytes: docx.MaxTemplateSize,
	}
}
