package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// requestInfo attaches the client address, user agent and request id to the
// context, for the audit events recorded while serving the request.
func requestInfo(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := audit.WithRequest(r.Context(), audit.RequestInfo{
				ClientIP:  clientIP(r, trusted),
				UserAgent: r.UserAgent(),
				RequestID: middleware.GetReqID(r.Context()),
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// clientIP returns the address of the client. X-Forwarded-For is only
// believed when the connection comes from a trusted proxy, and is read from
// the right: the first address not belonging to a trusted proxy is the
// client, since anything to its left was written by the client itself.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if !isTrusted(addr, trusted) {
		return addr.String()
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // garbage: stop at the last address we could trust
		}
		addr = hop.Unmap()
		if !isTrusted(addr, trusted) {
			break
		}
	}
	return addr.String()
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// record writes an audit event on behalf of the authenticated user.
func (h *handlers) record(r *http.Request, e domain.AuditEvent) {
	e.ActorID, e.ActorEmail = audit.Actor(auth.UserFrom(r.Context()))
	if e.Outcome == "" {
		e.Outcome = domain.AuditSuccess
	}
	h.d.Audit.Record(r.Context(), e)
}

// requireAdmin lets administrators through and records every refusal: an
// ordinary user probing the admin API is worth knowing about.
func (h *handlers) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.UserFrom(r.Context()).IsAdmin {
			h.record(r, domain.AuditEvent{
				Action: audit.ActionAdminAccess, Outcome: domain.AuditDenied,
				Details: map[string]any{"method": r.Method, "path": r.URL.Path},
			})
			writeError(w, r, http.StatusForbidden, "forbidden", "Administrator access is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------ admin: audit

type auditEventDTO struct {
	ID         uuid.UUID      `json:"id"`
	OccurredAt time.Time      `json:"occurredAt"`
	ActorID    *uuid.UUID     `json:"actorId,omitempty"`
	ActorEmail string         `json:"actorEmail,omitempty"`
	Action     string         `json:"action"`
	Outcome    string         `json:"outcome"`
	TargetType string         `json:"targetType,omitempty"`
	TargetID   string         `json:"targetId,omitempty"`
	ClientIP   string         `json:"clientIp,omitempty"`
	UserAgent  string         `json:"userAgent,omitempty"`
	RequestID  string         `json:"requestId,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

const auditPageSize = 100

func (h *handlers) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.AuditFilter{
		Actor:  strings.TrimSpace(q.Get("actor")),
		Action: strings.TrimSpace(q.Get("action")),
		Limit:  auditPageSize,
	}
	if len(f.Actor) > 200 || len(f.Action) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", "Filter value too long.")
		return
	}
	for key, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_filter", "Dates must be RFC 3339 timestamps.")
				return
			}
			*dst = &t
		}
	}
	if v := q.Get("before"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_filter", "Invalid pagination cursor.")
			return
		}
		f.Before = &id
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > auditPageSize {
			writeError(w, r, http.StatusBadRequest, "invalid_filter", "Invalid page size.")
			return
		}
		f.Limit = n
	}

	events, err := h.d.AuditLog.ListAuditEvents(r.Context(), f)
	if err != nil {
		handleError(w, r, err)
		return
	}
	// Reading the trail is itself audited: who looked at whose activity.
	details := map[string]any{}
	for key, v := range map[string]string{"actor": f.Actor, "action": f.Action, "from": q.Get("from"), "to": q.Get("to")} {
		if v != "" {
			details[key] = v
		}
	}
	if f.Before != nil {
		details["nextPage"] = true
	}
	h.record(r, domain.AuditEvent{Action: audit.ActionAuditRead, Details: details})

	out := make([]auditEventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, auditEventDTO{
			ID: e.ID, OccurredAt: e.OccurredAt, ActorID: e.ActorID, ActorEmail: e.ActorEmail,
			Action: e.Action, Outcome: string(e.Outcome), TargetType: e.TargetType, TargetID: e.TargetID,
			ClientIP: e.ClientIP, UserAgent: e.UserAgent, RequestID: e.RequestID, Details: e.Details,
		})
	}
	resp := map[string]any{"events": out}
	if len(events) == f.Limit {
		resp["nextCursor"] = events[len(events)-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}
