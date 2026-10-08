package notify

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

type outcome struct {
	err     error
	retryAt *time.Time
}

type fakeOutbox struct {
	batch    []domain.Delivery
	outcomes map[domain.Channel]outcome
}

func (f *fakeOutbox) ClaimDeliveries(context.Context, int, time.Duration) ([]domain.Delivery, error) {
	b := f.batch
	f.batch = nil
	return b, nil
}

func (f *fakeOutbox) FinishDelivery(_ context.Context, _ uuid.UUID, c domain.Channel, err error, retryAt *time.Time) error {
	f.outcomes[c] = outcome{err, retryAt}
	return nil
}

type fakeSender struct {
	err  error
	to   string
	sent int
}

func (f *fakeSender) Send(_ context.Context, to string, _ domain.Notification) error {
	f.sent++
	f.to = to
	return f.err
}

func TestDispatcher(t *testing.T) {
	sealer, _ := crypto.NewSealer(bytes.Repeat([]byte{4}, 32))
	user := uuid.New()
	url, _ := sealer.Seal([]byte("https://x.logic.azure.com/w"), teamsAD(user))
	n := domain.Notification{ID: uuid.New(), UserID: user, Title: "t"}

	cases := []struct {
		name       string
		emailErr   error
		attempts   int
		teamsURL   []byte
		wantRetry  bool
		wantFailed bool
	}{
		{name: "sent", attempts: 1, teamsURL: url},
		{name: "transient failure is retried", emailErr: errors.New("connection refused"), attempts: 1, teamsURL: url, wantRetry: true, wantFailed: true},
		{name: "permanent failure is not", emailErr: Permanent(errors.New("550")), attempts: 1, teamsURL: url, wantFailed: true},
		{name: "gives up after the last attempt", emailErr: errors.New("timeout"), attempts: maxAttempts, teamsURL: url, wantFailed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox := &fakeOutbox{outcomes: map[domain.Channel]outcome{}, batch: []domain.Delivery{
				{Notification: n, Channel: domain.ChannelEmail, Attempts: tc.attempts, Email: "a@example.com"},
				{Notification: n, Channel: domain.ChannelTeams, Attempts: 1, EncryptedTeamsWebhook: tc.teamsURL},
			}}
			email, teams := &fakeSender{err: tc.emailErr}, &fakeSender{}
			d := NewDispatcher(outbox, sealer, email, teams, time.Second)
			if n := d.RunOnce(context.Background()); n != 2 {
				t.Fatalf("handled %d deliveries", n)
			}
			got := outbox.outcomes[domain.ChannelEmail]
			if (got.err != nil) != tc.wantFailed || (got.retryAt != nil) != tc.wantRetry {
				t.Fatalf("email outcome: err=%v retry=%v", got.err, got.retryAt)
			}
			if teams.to != "https://x.logic.azure.com/w" || outbox.outcomes[domain.ChannelTeams].err != nil {
				t.Errorf("teams: to=%q outcome=%+v", teams.to, outbox.outcomes[domain.ChannelTeams])
			}
		})
	}
}

func TestDispatcherWithoutRecipient(t *testing.T) {
	sealer, _ := crypto.NewSealer(bytes.Repeat([]byte{4}, 32))
	n := domain.Notification{ID: uuid.New(), UserID: uuid.New()}
	outbox := &fakeOutbox{outcomes: map[domain.Channel]outcome{}, batch: []domain.Delivery{
		{Notification: n, Channel: domain.ChannelEmail, Attempts: 1, Email: ""},
		{Notification: n, Channel: domain.ChannelTeams, Attempts: 1},
	}}
	NewDispatcher(outbox, sealer, nil, &fakeSender{}, time.Second).RunOnce(context.Background())
	for c, o := range outbox.outcomes {
		if o.err == nil || o.retryAt != nil {
			t.Errorf("%s: a missing recipient is a permanent failure, got %+v", c, o)
		}
	}
}

func TestBackoff(t *testing.T) {
	if backoff(1) != time.Minute || backoff(3) != 4*time.Minute || backoff(20) != 4*time.Hour {
		t.Errorf("backoff: %v %v %v", backoff(1), backoff(3), backoff(20))
	}
}
