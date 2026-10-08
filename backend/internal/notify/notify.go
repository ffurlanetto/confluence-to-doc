// Package notify tells users what happened while they were away: an export
// is ready or failed, their Confluence token is about to expire, their
// inactive account is about to be deleted.
//
// Every notification is stored and shown in the application. It is also
// delivered on the external channels the user chose — email (when the server
// has SMTP) and Microsoft Teams (a workflow URL the user provides) — through
// an outbox: deliveries are rows, sent by a dispatcher on the workers and
// retried with backoff, so a mail server that is down delays a message
// instead of losing it or slowing an export.
//
// Messages never contain a document or its content: a title, a status and a
// link back to the application.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// Repo is the persistence the service needs.
type Repo interface {
	GetPreferences(ctx context.Context, userID uuid.UUID) (*domain.Preferences, error)
	GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error)
	CreateNotification(ctx context.Context, n domain.Notification, channels []domain.Channel) error
	SetNotificationPreferences(ctx context.Context, userID uuid.UUID, email, exports bool) error
	SetTeamsWebhook(ctx context.Context, userID uuid.UUID, encrypted []byte) error
	PATsExpiringBefore(ctx context.Context, cutoff time.Time, limit int) ([]domain.Preferences, error)
	MarkPATExpiryNotified(ctx context.Context, userID uuid.UUID) error
	ListNotifications(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Notification, int, error)
	MarkNotificationsRead(ctx context.Context, userID uuid.UUID) error
}

// Service creates notifications and manages the users' channel settings.
type Service struct {
	repo      Repo
	sealer    *crypto.Sealer
	publicURL string
	email     bool // SMTP is configured
	teams     *Teams
	now       func() time.Time
}

// NewService returns a service; emailEnabled says whether the server can send
// email, publicURL is the application's base URL used in links.
func NewService(repo Repo, sealer *crypto.Sealer, publicURL string, emailEnabled bool, teams *Teams) *Service {
	return &Service{repo: repo, sealer: sealer, publicURL: publicURL, email: emailEnabled, teams: teams, now: time.Now}
}

// EmailAvailable reports whether notifications can be sent by email.
func (s *Service) EmailAvailable() bool { return s.email }

// notify stores a notification for the user and queues it on their channels.
// It never fails the caller's operation: a notification that cannot be
// stored is logged.
func (s *Service) notify(ctx context.Context, userID uuid.UUID, kind domain.NotificationKind, title, body, path string) {
	prefs, err := s.repo.GetPreferences(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "notification not created: preferences unavailable", "user_id", userID, "err", err)
		return
	}
	var channels []domain.Channel
	wanted := !kind.Optional() || prefs.NotifyExports
	// Account notices go out by email even to users who turned email off:
	// they announce something the user would not want to miss.
	if s.email && wanted && (prefs.NotifyEmail || !kind.Optional()) {
		channels = append(channels, domain.ChannelEmail)
	}
	if len(prefs.EncryptedTeamsWebhook) > 0 && wanted {
		channels = append(channels, domain.ChannelTeams)
	}
	n := domain.Notification{
		ID: uuid.Must(uuid.NewV7()), UserID: userID, Kind: kind,
		Title: title, Body: body, Link: s.publicURL + path,
	}
	if err := s.repo.CreateNotification(context.WithoutCancel(ctx), n, channels); err != nil {
		slog.WarnContext(ctx, "notification not created", "user_id", userID, "kind", kind, "err", err)
	}
}

const dateTime = "2 Jan 2006, 15:04 MST"

// ExportSucceeded tells the requester their document is ready.
func (s *Service) ExportSucceeded(ctx context.Context, job *domain.Export, expires time.Time) {
	s.notify(ctx, job.UserID, domain.NotifyExportSucceeded,
		fmt.Sprintf("Your export of “%s” is ready", job.RootTitle),
		fmt.Sprintf("The %s document is ready to download until %s.", formatName(job.Format), expires.UTC().Format(dateTime)),
		"/")
}

// ExportFailed tells the requester their export could not be generated.
func (s *Service) ExportFailed(ctx context.Context, job *domain.Export, reason string) {
	s.notify(ctx, job.UserID, domain.NotifyExportFailed,
		fmt.Sprintf("Your export of “%s” failed", job.RootTitle),
		reason, "/")
}

// InactiveAccountWarning implements account.Notifier.
func (s *Service) InactiveAccountWarning(ctx context.Context, u domain.User, deletion time.Time) error {
	s.notify(ctx, u.ID, domain.NotifyAccountInactive,
		"Your Confluence Export account will be deleted",
		fmt.Sprintf("You have not signed in for a long time. Your account, your Confluence token and your exports "+
			"will be deleted on %s. Sign in before then to keep them.", deletion.UTC().Format("2 January 2006")),
		"/")
	return nil
}

// patBatch bounds one pass of NotifyExpiringTokens.
const patBatch = 100

// NotifyExpiringTokens warns, once, the users whose Confluence token expires
// within the coming period.
func (s *Service) NotifyExpiringTokens(ctx context.Context, within time.Duration) (int, error) {
	expiring, err := s.repo.PATsExpiringBefore(ctx, s.now().Add(within), patBatch)
	if err != nil {
		return 0, err
	}
	for _, p := range expiring {
		verb := "expires"
		if p.PATExpiresAt.Before(s.now()) {
			verb = "expired"
		}
		s.notify(ctx, p.UserID, domain.NotifyPATExpiring,
			"Your Confluence token "+verb+" on "+p.PATExpiresAt.UTC().Format("2 January 2006"),
			"Exports stop working when the personal access token expires. Create a new token in Confluence and save "+
				"it in your Confluence Export preferences.",
			"/settings")
		if err := s.repo.MarkPATExpiryNotified(ctx, p.UserID); err != nil {
			return 0, err
		}
	}
	return len(expiring), nil
}

func formatName(f domain.Format) string {
	if f == domain.FormatDOCX {
		return "Word"
	}
	return "PDF"
}

// List returns the user's latest notifications and the unread count.
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]domain.Notification, int, error) {
	return s.repo.ListNotifications(ctx, userID, 50)
}

// MarkRead marks all the user's notifications read.
func (s *Service) MarkRead(ctx context.Context, userID uuid.UUID) error {
	return s.repo.MarkNotificationsRead(ctx, userID)
}

// --- Channel settings ------------------------------------------------------------

// SetPreferences stores which notifications the user wants by email and
// whether finished exports are notified at all.
func (s *Service) SetPreferences(ctx context.Context, userID uuid.UUID, email, exports bool) error {
	return s.repo.SetNotificationPreferences(ctx, userID, email, exports)
}

// ErrInvalidTeamsURL is returned for a URL that is not an allowed Teams
// workflow endpoint.
var ErrInvalidTeamsURL = errors.New("not an allowed Microsoft Teams workflow URL")

// SetTeamsWebhook validates and stores the user's Teams workflow URL.
func (s *Service) SetTeamsWebhook(ctx context.Context, userID uuid.UUID, rawURL string) error {
	if err := s.teams.Validate(rawURL); err != nil {
		return err
	}
	enc, err := s.sealer.Seal([]byte(rawURL), teamsAD(userID))
	if err != nil {
		return err
	}
	return s.repo.SetTeamsWebhook(ctx, userID, enc)
}

// ClearTeamsWebhook removes the user's Teams workflow URL.
func (s *Service) ClearTeamsWebhook(ctx context.Context, userID uuid.UUID) error {
	return s.repo.SetTeamsWebhook(ctx, userID, nil)
}

// TestTeams sends a test message to the user's Teams workflow, now.
func (s *Service) TestTeams(ctx context.Context, userID uuid.UUID) error {
	prefs, err := s.repo.GetPreferences(ctx, userID)
	if err != nil {
		return err
	}
	if len(prefs.EncryptedTeamsWebhook) == 0 {
		return domain.ErrNotFound
	}
	url, err := s.sealer.Open(prefs.EncryptedTeamsWebhook, teamsAD(userID))
	if err != nil {
		return err
	}
	return s.teams.Send(ctx, string(url), domain.Notification{
		Title: "Confluence Export is connected",
		Body:  "Your export notifications will appear here.",
		Link:  s.publicURL + "/",
	})
}

// teamsAD binds the encrypted URL to its user and to its purpose, so it
// cannot be swapped with the same user's encrypted PAT.
func teamsAD(userID uuid.UUID) []byte { return append(userID[:], "teams"...) }
