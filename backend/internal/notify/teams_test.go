package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

func TestTeamsValidate(t *testing.T) {
	teams := NewTeams(DefaultTeamsHosts, nil)
	ok := []string{
		"https://prod-12.westeurope.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?sig=x",
		"https://default1234.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/x",
		"https://acme.webhook.office.com/webhookb2/x",
	}
	for _, u := range ok {
		if err := teams.Validate(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	bad := []string{
		"http://prod.logic.azure.com/x",            // not https
		"https://evil.com/logic.azure.com",         // host is evil.com
		"https://logic.azure.com.evil.com/x",       // suffix trick
		"https://evilwebhook.office.com.evil/x",    // suffix trick
		"https://user:pw@prod.logic.azure.com/x",   // credentials
		"https://prod.logic.azure.com:8443/x",      // other port
		"https://169.254.169.254/latest/meta-data", // metadata endpoint
		"not a url",
	}
	for _, u := range bad {
		if err := teams.Validate(u); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

func TestTeamsSend(t *testing.T) {
	var got map[string]any
	status := http.StatusAccepted
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		if status == http.StatusFound {
			http.Redirect(w, r, "https://169.254.169.254/", status)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	teams := NewTeams([]string{u.Hostname()}, srv.Client().Transport)
	// httptest listens on a random port; the port check is relaxed for it.
	target := srv.URL + "/workflow"
	n := domain.Notification{Title: "Your export is ready", Body: "Ready until 10 Oct.", Link: "https://app.example.com/"}

	err := teams.sendTo(context.Background(), target, n)
	if err != nil {
		t.Fatal(err)
	}
	card := got["attachments"].([]any)[0].(map[string]any)["content"].(map[string]any)
	if card["type"] != "AdaptiveCard" || card["body"].([]any)[0].(map[string]any)["text"] != n.Title {
		t.Fatalf("unexpected card: %v", got)
	}

	for code, permanent := range map[int]bool{http.StatusBadRequest: true, http.StatusNotFound: true,
		http.StatusTooManyRequests: false, http.StatusBadGateway: false, http.StatusFound: true} {
		status = code
		err := teams.sendTo(context.Background(), target, n)
		if err == nil || IsPermanent(err) != permanent {
			t.Errorf("%d: err=%v permanent=%v, want permanent=%v", code, err, IsPermanent(err), permanent)
		}
	}
}
