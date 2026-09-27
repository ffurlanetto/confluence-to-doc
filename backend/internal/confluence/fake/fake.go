// Package fake provides an in-memory Confluence REST API implementing the
// subset used by this application. It backs unit/integration tests and the
// local development stack (cmd/fakeconfluence).
package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type Page struct {
	ID       string
	Title    string
	SpaceKey string
	Body     string
	ParentID string
}

type Server struct {
	Token string

	mu          sync.RWMutex
	pages       map[string]*Page
	children    map[string][]string
	attachments map[string][]byte

	// Requests counts API calls, handy to assert pagination/caching behaviour.
	Requests atomic.Int64
	// FailNext makes the next N requests return 503 (to test retries).
	FailNext atomic.Int32
}

func New(token string) *Server {
	return &Server{
		Token:       token,
		pages:       map[string]*Page{},
		children:    map[string][]string{},
		attachments: map[string][]byte{},
	}
}

// AddPage registers a page; pages are listed in insertion order under their parent.
func (s *Server) AddPage(p Page) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.SpaceKey == "" {
		p.SpaceKey = "DEMO"
	}
	s.pages[p.ID] = &p
	if p.ParentID != "" {
		s.children[p.ParentID] = append(s.children[p.ParentID], p.ID)
	}
}

// AddAttachment serves data at /download/attachments/<name>.
func (s *Server) AddAttachment(name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachments[name] = data
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Requests.Add(1)
	if s.FailNext.Load() > 0 {
		s.FailNext.Add(-1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/rest/api/user/current":
		writeJSON(w, map[string]string{"username": "jdoe", "displayName": "John Doe"})
	case p == "/rest/api/content/search":
		s.search(w, r)
	case p == "/rest/api/content":
		s.byTitle(w, r)
	case strings.HasPrefix(p, "/rest/api/content/") && strings.HasSuffix(p, "/child/page"):
		s.listChildren(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "/rest/api/content/"), "/child/page"))
	case strings.HasPrefix(p, "/rest/api/content/"):
		s.getPage(w, r, strings.TrimPrefix(p, "/rest/api/content/"))
	case strings.HasPrefix(p, "/download/attachments/"):
		s.mu.RLock()
		data, ok := s.attachments[strings.TrimPrefix(p, "/download/attachments/")]
		s.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(data))
		_, _ = w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) render(p *Page, expand string) map[string]any {
	out := map[string]any{
		"id":      p.ID,
		"type":    "page",
		"title":   p.Title,
		"space":   map[string]string{"key": p.SpaceKey, "name": p.SpaceKey + " space"},
		"version": map[string]int{"number": 1},
		"_links":  map[string]string{"webui": "/pages/viewpage.action?pageId=" + p.ID},
	}
	if strings.Contains(expand, "body.export_view") {
		out["body"] = map[string]any{"export_view": map[string]string{"value": p.Body}}
	}
	return out
}

func (s *Server) getPage(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pages[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, s.render(p, r.URL.Query().Get("expand")))
}

func (s *Server) listChildren(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.pages[id]; !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	start, _ := strconv.Atoi(q.Get("start"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 25 {
		limit = 25 // real Confluence caps page size when bodies are expanded
	}
	ids := s.children[id]
	end := min(start+limit, len(ids))
	results := []map[string]any{}
	if start < len(ids) {
		for _, cid := range ids[start:end] {
			results = append(results, s.render(s.pages[cid], q.Get("expand")))
		}
	}
	links := map[string]string{}
	if end < len(ids) {
		links["next"] = fmt.Sprintf("/rest/api/content/%s/child/page?start=%d&limit=%d", id, end, limit)
	}
	writeJSON(w, map[string]any{"results": results, "start": start, "limit": limit, "size": len(results), "_links": links})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	cql := r.URL.Query().Get("cql")
	// Extremely small CQL subset: title ~ "x" [AND space="KEY"].
	term := between(cql, `title ~ "`, `"`)
	space := between(cql, `space="`, `"`)
	s.mu.RLock()
	defer s.mu.RUnlock()
	results := []map[string]any{}
	ids := make([]string, 0, len(s.pages))
	for id := range s.pages {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := s.pages[id]
		if strings.Contains(strings.ToLower(p.Title), strings.ToLower(term)) && (space == "" || p.SpaceKey == space) {
			results = append(results, s.render(p, ""))
		}
	}
	writeJSON(w, map[string]any{"results": results, "size": len(results)})
}

func (s *Server) byTitle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.RLock()
	defer s.mu.RUnlock()
	results := []map[string]any{}
	for _, p := range s.pages {
		if p.SpaceKey == q.Get("spaceKey") && p.Title == q.Get("title") {
			results = append(results, s.render(p, ""))
		}
	}
	writeJSON(w, map[string]any{"results": results, "size": len(results)})
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	if j := strings.Index(s, end); j >= 0 {
		return s[:j]
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
