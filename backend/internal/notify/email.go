package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// SMTPConfig is the mail relay notifications go through.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// Security is "starttls" (upgrade a plain connection, port 587), "tls"
	// (implicit TLS, port 465) or "none" (a local relay only).
	Security string
}

// Email sends notifications through an SMTP relay.
type Email struct {
	cfg  SMTPConfig
	from *mail.Address
	now  func() time.Time
}

func NewEmail(cfg SMTPConfig) (*Email, error) {
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("notify: invalid sender address %q: %w", cfg.From, err)
	}
	return &Email{cfg: cfg, from: from, now: time.Now}, nil
}

// Send mails n to address.
func (e *Email) Send(ctx context.Context, address string, n domain.Notification) error {
	to, err := mail.ParseAddress(address)
	if err != nil {
		return Permanent(fmt.Errorf("email: invalid recipient: %w", err))
	}
	msg, err := e.message(to, n)
	if err != nil {
		return Permanent(err)
	}
	conn, err := e.dial(ctx)
	if err != nil {
		return fmt.Errorf("email: connecting to %s: %w", e.cfg.Host, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(e.now().Add(30 * time.Second))
	}
	c, err := smtp.NewClient(conn, e.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("email: %w", err)
	}
	defer c.Close()
	if e.cfg.Security == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: e.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("email: STARTTLS: %w", err)
		}
	}
	if e.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, e.cfg.Host)); err != nil {
			return Permanent(fmt.Errorf("email: authentication: %w", err))
		}
	}
	if err := c.Mail(e.from.Address); err != nil {
		return classify(err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return classify(err)
	}
	w, err := c.Data()
	if err != nil {
		return classify(err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if err := w.Close(); err != nil {
		return classify(err)
	}
	return c.Quit()
}

func (e *Email) dial(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort(e.cfg.Host, strconv.Itoa(e.cfg.Port))
	d := &net.Dialer{Timeout: 15 * time.Second}
	if e.cfg.Security == "tls" {
		td := &tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: e.cfg.Host, MinVersion: tls.VersionTLS12}}
		return td.DialContext(ctx, "tcp", addr)
	}
	return d.DialContext(ctx, "tcp", addr)
}

// classify makes 5xx SMTP replies permanent: the relay will not change its
// mind about that recipient.
func classify(err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 500 {
		return Permanent(fmt.Errorf("email: %w", err))
	}
	return fmt.Errorf("email: %w", err)
}

// message builds a plain-text UTF-8 message. Header values come from the
// notification, so line breaks are removed from them: no header injection.
func (e *Email) message(to *mail.Address, n domain.Notification) ([]byte, error) {
	var body bytes.Buffer
	qp := quotedprintable.NewWriter(&body)
	text := n.Body
	if n.Link != "" {
		text += "\n\n" + n.Link
	}
	text += "\n\n--\nConfluence Export. You receive this message because of your notification preferences."
	if _, err := qp.Write([]byte(text)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	domainPart := e.from.Address[strings.LastIndex(e.from.Address, "@")+1:]
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", e.from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", oneLine(n.Title)))
	header("Date", e.now().Format(time.RFC1123Z))
	header("Message-ID", "<"+uuid.NewString()+"@"+domainPart+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	b.Write(bytes.ReplaceAll(body.Bytes(), []byte("\n"), []byte("\r\n")))
	return b.Bytes(), nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}
