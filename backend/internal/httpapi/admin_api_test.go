package httpapi_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// wordTemplate builds the smallest package the template validation accepts,
// with extra parts added or replaced.
func wordTemplate(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	const w = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.template.main+xml"/></Types>`,
		"word/document.xml":            `<?xml version="1.0"?><w:document ` + w + `><w:body><w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr></w:body></w:document>`,
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`,
		"word/styles.xml":              `<?xml version="1.0"?><w:styles ` + w + `><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style></w:styles>`,
	}
	for k, v := range extra {
		parts[k] = v
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range parts {
		f, _ := zw.Create(name)
		_, _ = f.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// upload sends a template as the raw request body.
func (a *api) upload(user, name string, content []byte, out any) *http.Response {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodPut, a.srv.URL+"/api/admin/template?name="+url.QueryEscape(name), bytes.NewReader(content))
	req.Header.Set("X-Test-User", user)
	req.Header.Set("X-CSRF-Protection", "1")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		decodeInto(a.t, resp, out)
	}
	return resp
}

func TestAdminRoutesRequireAdmin(t *testing.T) {
	a := newAPIWithWorker(t, false)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/admin/exports"},
		{"POST", "/api/admin/exports/0199aaaa-0000-7000-8000-000000000000/cancel"},
		{"POST", "/api/admin/exports/0199aaaa-0000-7000-8000-000000000000/retry"},
		{"GET", "/api/admin/users"},
		{"POST", "/api/admin/users/0199aaaa-0000-7000-8000-000000000000/block"},
		{"DELETE", "/api/admin/users/0199aaaa-0000-7000-8000-000000000000/block"},
		{"GET", "/api/admin/usage"},
		{"GET", "/api/admin/template"},
		{"DELETE", "/api/admin/template"},
	} {
		if r := a.do(route.method, route.path, "bob", map[string]string{"reason": "x"}, nil); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a user = %d, want 403", route.method, route.path, r.StatusCode)
		}
	}
	if r := a.upload("bob", "t.dotx", wordTemplate(t, nil), nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("template upload as a user = %d, want 403", r.StatusCode)
	}
}

type queuePage struct {
	Exports []struct {
		ID, Status, Error, OwnerEmail string
	}
}

func TestAdminQueueCancelRetry(t *testing.T) {
	a := newAPIWithWorker(t, false) // nothing runs: exports stay queued
	a.do("PUT", "/api/preferences/pat", "alice", map[string]string{"token": "good-pat"}, nil)
	var created struct{ ID string }
	a.do("POST", "/api/exports", "alice", map[string]any{"pageId": "10", "format": "pdf"}, &created)

	var q queuePage
	if r := a.do("GET", "/api/admin/exports?status=queued,running", "admin-ann", nil, &q); r.StatusCode != http.StatusOK {
		t.Fatalf("queue = %d", r.StatusCode)
	}
	if len(q.Exports) != 1 || q.Exports[0].ID != created.ID || q.Exports[0].OwnerEmail != "alice@example.com" {
		t.Fatalf("queue = %+v", q)
	}
	if r := a.do("GET", "/api/admin/exports?status=bogus", "admin-ann", nil, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown status = %d, want 400", r.StatusCode)
	}

	var e struct{ Status, Error string }
	if r := a.do("POST", "/api/admin/exports/"+created.ID+"/cancel", "admin-ann", nil, &e); r.StatusCode != http.StatusOK || e.Status != "failed" {
		t.Fatalf("cancel = %d %+v", r.StatusCode, e)
	}
	// The owner sees why.
	a.do("GET", "/api/exports/"+created.ID, "alice", nil, &e)
	if e.Error != "Cancelled by an administrator." {
		t.Fatalf("owner sees %+v", e)
	}
	var err apiError
	if r := a.do("POST", "/api/admin/exports/"+created.ID+"/cancel", "admin-ann", nil, &err); r.StatusCode != http.StatusConflict || err.Error.Code != "invalid_state" {
		t.Fatalf("second cancel = %d %+v", r.StatusCode, err)
	}
	if r := a.do("POST", "/api/admin/exports/"+created.ID+"/retry", "admin-ann", nil, &e); r.StatusCode != http.StatusOK || e.Status != "queued" {
		t.Fatalf("retry = %d %+v", r.StatusCode, e)
	}
	if r := a.do("POST", "/api/admin/exports/not-a-uuid/retry", "admin-ann", nil, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("bad id = %d, want 404", r.StatusCode)
	}

	var page auditPage
	a.do("GET", "/api/admin/audit?action=admin.export.cancel", "admin-ann", nil, &page)
	if got := page.actions(); len(got) != 2 || got[0] != "admin.export.cancel:failure" || got[1] != "admin.export.cancel:success" {
		t.Fatalf("cancellations audited = %v", got)
	}
}

func TestAdminBlocksAUser(t *testing.T) {
	a := newAPIWithWorker(t, false)
	a.do("GET", "/api/me", "carol", nil, nil)
	var users struct {
		Users []struct {
			ID, Email     string
			BlockedReason string
			BlockedAt     *time.Time
		}
	}
	a.do("GET", "/api/admin/users?q=carol", "admin-ann", nil, &users)
	if len(users.Users) != 1 || users.Users[0].Email != "carol@example.com" {
		t.Fatalf("users = %+v", users)
	}
	carol := users.Users[0].ID

	if r := a.do("POST", "/api/admin/users/"+carol+"/block", "admin-ann", map[string]string{"reason": " "}, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("block without reason = %d, want 400", r.StatusCode)
	}
	if r := a.do("POST", "/api/admin/users/"+carol+"/block", "admin-ann", map[string]string{"reason": "Left the company"}, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("block = %d", r.StatusCode)
	}
	a.do("GET", "/api/admin/users?q=carol", "admin-ann", nil, &users)
	if users.Users[0].BlockedAt == nil || users.Users[0].BlockedReason != "Left the company" {
		t.Fatalf("blocked user = %+v", users.Users[0])
	}
	if r := a.do("DELETE", "/api/admin/users/"+carol+"/block", "admin-ann", nil, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("unblock = %d", r.StatusCode)
	}

	// An administrator cannot lock themselves out.
	a.do("GET", "/api/admin/users?q=admin-ann", "admin-ann", nil, &users)
	var err apiError
	if r := a.do("POST", "/api/admin/users/"+users.Users[0].ID+"/block", "admin-ann", map[string]string{"reason": "oops"}, &err); r.StatusCode != http.StatusConflict || err.Error.Code != "self_action" {
		t.Fatalf("self block = %d %+v", r.StatusCode, err)
	}

	var page auditPage
	a.do("GET", "/api/admin/audit?action=admin.user.block", "admin-ann", nil, &page)
	if len(page.Events) != 1 || page.Events[0].TargetID != carol || page.Events[0].Details["reason"] != "Left the company" {
		t.Fatalf("block audited = %+v", page.Events)
	}
}

func TestAdminUsage(t *testing.T) {
	a := newAPI(t)
	a.do("PUT", "/api/preferences/pat", "alice", map[string]string{"token": "good-pat"}, nil)
	var created struct{ ID string }
	a.do("POST", "/api/exports", "alice", map[string]any{"pageId": "10", "format": "docx", "includeChildren": true}, &created)
	waitFor(t, a, "alice", created.ID, "succeeded")

	var u struct {
		Succeeded, Failed, Users int
		Pages                    int64
		ByFormat                 map[string]int
		Daily                    []struct{ Day string }
		TopUsers                 []struct{ Email string }
	}
	if r := a.do("GET", "/api/admin/usage?days=7", "admin-ann", nil, &u); r.StatusCode != http.StatusOK {
		t.Fatalf("usage = %d", r.StatusCode)
	}
	if u.Succeeded != 1 || u.Pages != 2 || u.Users != 1 || u.ByFormat["docx"] != 1 || len(u.Daily) != 1 ||
		u.Daily[0].Day != time.Now().UTC().Format(time.DateOnly) || len(u.TopUsers) != 1 || u.TopUsers[0].Email != "alice@example.com" {
		t.Fatalf("usage = %+v", u)
	}
	if r := a.do("GET", "/api/admin/usage?days=0", "admin-ann", nil, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("days=0 = %d, want 400", r.StatusCode)
	}
}

func TestAdminWordTemplate(t *testing.T) {
	a := newAPIWithWorker(t, false)
	var info struct {
		Origin, Name, UploadedBy string
		MaxBytes                 int
	}
	a.do("GET", "/api/admin/template", "admin-ann", nil, &info)
	if info.Origin != "none" || info.MaxBytes == 0 {
		t.Fatalf("initial template = %+v", info)
	}

	if r := a.upload("admin-ann", "ACME.dotx", wordTemplate(t, nil), &info); r.StatusCode != http.StatusOK ||
		info.Origin != "uploaded" || info.Name != "ACME.dotx" || info.UploadedBy != "admin-ann@example.com" {
		t.Fatalf("upload = %d %+v", r.StatusCode, info)
	}
	// Users see which template their documents get.
	var prefs struct{ DocumentTemplate string }
	a.do("GET", "/api/preferences", "alice", nil, &prefs)
	if prefs.DocumentTemplate != "ACME.dotx" {
		t.Fatalf("preferences template = %q", prefs.DocumentTemplate)
	}

	var e apiError
	for name, tc := range map[string]struct {
		file    string
		content []byte
		code    string
	}{
		"not a template": {"notes.docx", []byte("hello"), "invalid_template"},
		"wrong type":     {"notes.pdf", wordTemplate(t, nil), "invalid_template"},
		"too large":      {"big.docx", make([]byte, 10<<20+1), "invalid_template"},
		"external image": {"evil.dotx", wordTemplate(t, map[string]string{
			"word/_rels/document.xml.rels": `<Relationships><Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="https://evil.example/p.png" TargetMode="External"/></Relationships>`,
		}), "unsafe_template"},
	} {
		if r := a.upload("admin-ann", tc.file, tc.content, &e); r.StatusCode != http.StatusBadRequest || e.Error.Code != tc.code {
			t.Errorf("%s: %d %+v, want 400 %s", name, r.StatusCode, e, tc.code)
		}
	}
	a.do("GET", "/api/admin/template", "admin-ann", nil, &info)
	if info.Name != "ACME.dotx" {
		t.Fatalf("a refused upload must keep the template in use, got %+v", info)
	}

	if r := a.do("DELETE", "/api/admin/template", "admin-ann", nil, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("reset = %d", r.StatusCode)
	}
	if r := a.do("DELETE", "/api/admin/template", "admin-ann", nil, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("second reset = %d, want 404", r.StatusCode)
	}

	var page auditPage
	a.do("GET", "/api/admin/audit?action=admin.template.upload", "admin-ann", nil, &page)
	if got := fmt.Sprint(page.actions()); got != "[admin.template.upload:failure admin.template.upload:failure admin.template.upload:failure admin.template.upload:failure admin.template.upload:success]" {
		t.Fatalf("uploads audited = %s", got)
	}
}

func decodeInto(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
}

// waitFor polls the export until it reaches status.
func waitFor(t *testing.T, a *api, user, id, status string) {
	t.Helper()
	var got struct{ Status string }
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if a.do("GET", "/api/exports/"+id, user, nil, &got); got.Status == status {
			return
		}
	}
	t.Fatalf("export %s is %q, want %q", id, got.Status, status)
}
