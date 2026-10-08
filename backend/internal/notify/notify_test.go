package notify

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

type memRepo struct {
	prefs    domain.Preferences
	created  []domain.Notification
	channels [][]domain.Channel
	expiring []domain.Preferences
	marked   []uuid.UUID
}

func (m *memRepo) GetPreferences(context.Context, uuid.UUID) (*domain.Preferences, error) {
	p := m.prefs
	return &p, nil
}
func (m *memRepo) GetUser(context.Context, uuid.UUID) (*domain.User, error) {
	return &domain.User{}, nil
}
func (m *memRepo) CreateNotification(_ context.Context, n domain.Notification, c []domain.Channel) error {
	m.created = append(m.created, n)
	m.channels = append(m.channels, c)
	return nil
}
func (m *memRepo) SetNotificationPreferences(context.Context, uuid.UUID, bool, bool) error {
	return nil
}
func (m *memRepo) SetTeamsWebhook(_ context.Context, _ uuid.UUID, enc []byte) error {
	m.prefs.EncryptedTeamsWebhook = enc
	return nil
}
func (m *memRepo) PATsExpiringBefore(context.Context, time.Time, int) ([]domain.Preferences, error) {
	return m.expiring, nil
}
func (m *memRepo) MarkPATExpiryNotified(_ context.Context, id uuid.UUID) error {
	m.marked = append(m.marked, id)
	return nil
}
func (m *memRepo) ListNotifications(context.Context, uuid.UUID, int) ([]domain.Notification, int, error) {
	return nil, 0, nil
}
func (m *memRepo) MarkNotificationsRead(context.Context, uuid.UUID) error { return nil }

func TestChannelsFollowPreferences(t *testing.T) {
	sealer, _ := crypto.NewSealer(bytes.Repeat([]byte{4}, 32))
	job := &domain.Export{ID: uuid.New(), UserID: uuid.New(), RootTitle: "Guide", Format: domain.FormatPDF}
	cases := []struct {
		name           string
		email, exports bool
		teams          bool
		emailServer    bool
		account        bool
		want           string
	}{
		{name: "both channels", email: true, exports: true, teams: true, emailServer: true, want: "email,teams"},
		{name: "email off", email: false, exports: true, teams: true, emailServer: true, want: "teams"},
		{name: "no SMTP on the server", email: true, exports: true, emailServer: false, want: ""},
		{name: "exports muted", email: true, exports: false, teams: true, emailServer: true, want: ""},
		{name: "account notice ignores the mutes", email: false, exports: false, teams: true, emailServer: true, account: true, want: "email,teams"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &memRepo{prefs: domain.Preferences{NotifyEmail: tc.email, NotifyExports: tc.exports}}
			if tc.teams {
				repo.prefs.EncryptedTeamsWebhook = []byte("x")
			}
			s := NewService(repo, sealer, "https://app.example.com", tc.emailServer, NewTeams(DefaultTeamsHosts, nil))
			if tc.account {
				_ = s.InactiveAccountWarning(context.Background(), domain.User{ID: job.UserID}, time.Now().Add(15*24*time.Hour))
			} else {
				s.ExportSucceeded(context.Background(), job, time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
			}
			if len(repo.created) != 1 {
				t.Fatal("the notification is always stored, for the application")
			}
			var got []string
			for _, c := range repo.channels[0] {
				got = append(got, string(c))
			}
			if strings.Join(got, ",") != tc.want {
				t.Fatalf("channels = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestMessages(t *testing.T) {
	sealer, _ := crypto.NewSealer(bytes.Repeat([]byte{4}, 32))
	repo := &memRepo{prefs: domain.Preferences{NotifyEmail: true, NotifyExports: true}}
	s := NewService(repo, sealer, "https://app.example.com", true, NewTeams(DefaultTeamsHosts, nil))
	job := &domain.Export{UserID: uuid.New(), RootTitle: "Guide", Format: domain.FormatDOCX}
	s.ExportSucceeded(context.Background(), job, time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	s.ExportFailed(context.Background(), job, "The Confluence page cannot be found.")
	ok, failed := repo.created[0], repo.created[1]
	if ok.Title != "Your export of “Guide” is ready" || !strings.Contains(ok.Body, "Word document") ||
		!strings.Contains(ok.Body, "10 Oct 2026, 09:00 UTC") || ok.Link != "https://app.example.com/" {
		t.Errorf("success: %+v", ok)
	}
	if failed.Kind != domain.NotifyExportFailed || failed.Body != "The Confluence page cannot be found." {
		t.Errorf("failure: %+v", failed)
	}

	expiry := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	repo.expiring = []domain.Preferences{{UserID: uuid.New(), PATExpiresAt: &expiry}}
	if n, err := s.NotifyExpiringTokens(context.Background(), 14*24*time.Hour); err != nil || n != 1 || len(repo.marked) != 1 {
		t.Fatalf("expiring tokens: n=%d err=%v marked=%v", n, err, repo.marked)
	}
	if last := repo.created[len(repo.created)-1]; last.Link != "https://app.example.com/settings" || !strings.Contains(last.Title, "20 October 2026") {
		t.Errorf("token notice: %+v", last)
	}
}

func TestTeamsURLIsStoredEncrypted(t *testing.T) {
	sealer, _ := crypto.NewSealer(bytes.Repeat([]byte{4}, 32))
	repo := &memRepo{}
	s := NewService(repo, sealer, "https://app.example.com", false, NewTeams(DefaultTeamsHosts, nil))
	user := uuid.New()
	if err := s.SetTeamsWebhook(context.Background(), user, "https://evil.example.com/hook"); !errors.Is(err, ErrInvalidTeamsURL) {
		t.Fatalf("want ErrInvalidTeamsURL, got %v", err)
	}
	const url = "https://prod.westeurope.logic.azure.com/workflows/abc?sig=secret"
	if err := s.SetTeamsWebhook(context.Background(), user, url); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(repo.prefs.EncryptedTeamsWebhook, []byte("secret")) {
		t.Fatal("the URL must be encrypted")
	}
	if _, err := sealer.Open(repo.prefs.EncryptedTeamsWebhook, user[:]); err == nil {
		t.Fatal("the Teams URL must not open as a PAT would: it is bound to its purpose")
	}
}
