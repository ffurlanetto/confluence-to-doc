package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/account"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

type errorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"requestId,omitempty"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	var b errorBody
	b.Error.Code, b.Error.Message = code, message
	b.Error.RequestID = middleware.GetReqID(r.Context())
	writeJSON(w, status, b)
}

// handleError maps domain and upstream errors to HTTP responses. Unknown
// errors are logged with the request id and reported as a generic 500.
func handleError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "Resource not found.")
	case errors.Is(err, domain.ErrPATMissing):
		writeError(w, r, http.StatusConflict, "pat_missing", "Set your Confluence personal access token (PAT) in your preferences.")
	case errors.Is(err, account.ErrInvalidPAT), errors.Is(err, confluence.ErrUnauthorized):
		writeError(w, r, http.StatusConflict, "pat_invalid", "The Confluence personal access token is invalid or expired.")
	case errors.Is(err, confluence.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "confluence_forbidden", "Confluence denied access.")
	case errors.Is(err, confluence.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "page_not_found", "Confluence page not found.")
	case errors.Is(err, domain.ErrTooManyActive):
		writeError(w, r, http.StatusTooManyRequests, "too_many_exports", "You already have too many exports in progress. Try again once they finish.")
	case errors.Is(err, domain.ErrExportNotReady):
		writeError(w, r, http.StatusConflict, "not_ready", "The document is not ready yet.")
	case errors.Is(err, domain.ErrExportExpired):
		writeError(w, r, http.StatusGone, "expired", "The document has expired and is no longer available.")
	case errors.Is(err, domain.ErrInvalidFormat):
		writeError(w, r, http.StatusBadRequest, "invalid_format", "Invalid format (expected pdf or docx).")
	case errors.Is(err, domain.ErrInvalidPageID):
		writeError(w, r, http.StatusBadRequest, "invalid_page", "Invalid page identifier.")
	default:
		var se *confluence.StatusError
		if errors.As(err, &se) {
			writeError(w, r, http.StatusBadGateway, "confluence_error", "Confluence returned an error.")
			return
		}
		slog.ErrorContext(r.Context(), "request failed", "err", err, "request_id", middleware.GetReqID(r.Context()))
		writeError(w, r, http.StatusInternalServerError, "internal", "Internal error.")
	}
}

// errorReason is a short, stable label for an error, safe to record in the
// audit trail (no upstream message, which could echo user input).
func errorReason(err error) string {
	switch {
	case errors.Is(err, account.ErrInvalidPAT), errors.Is(err, confluence.ErrUnauthorized):
		return "pat_invalid"
	case errors.Is(err, confluence.ErrForbidden):
		return "confluence_forbidden"
	default:
		return "error"
	}
}
