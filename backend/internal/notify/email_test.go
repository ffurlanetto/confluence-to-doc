package notify

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// fakeSMTP is a minimal SMTP server: enough of the protocol for net/smtp to
// deliver one message, which it records.
type fakeSMTP struct {
	ln       net.Listener
	rcptCode string // reply to RCPT, "250" by default
	messages chan string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, rcptCode: "250", messages: make(chan string, 4)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			reply("250 fake")
		case strings.HasPrefix(cmd, "MAIL"):
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT"):
			reply(f.rcptCode + " rcpt")
		case cmd == "DATA":
			reply("354 go ahead")
			var msg strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				msg.WriteString(l)
			}
			f.messages <- msg.String()
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func TestEmailSend(t *testing.T) {
	srv := newFakeSMTP(t)
	_, port, _ := net.SplitHostPort(srv.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	mailer, err := NewEmail(SMTPConfig{Host: "127.0.0.1", Port: p, From: "Confluence Export <noreply@example.com>", Security: "none"})
	if err != nil {
		t.Fatal(err)
	}
	n := domain.Notification{
		Title: "Your export of “Guide”\r\nBcc: victim@example.com is ready",
		Body:  "The PDF document is ready.",
		Link:  "https://app.example.com/",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mailer.Send(ctx, "alice@example.com", n); err != nil {
		t.Fatal(err)
	}
	msg := <-srv.messages
	head, body, _ := strings.Cut(msg, "\r\n\r\n")
	for _, want := range []string{"From: \"Confluence Export\" <noreply@example.com>", "To: <alice@example.com>",
		"Subject: =?utf-8?q?", "Auto-Submitted: auto-generated", "Content-Type: text/plain; charset=utf-8"} {
		if !strings.Contains(head, want) {
			t.Errorf("header %q missing:\n%s", want, head)
		}
	}
	if strings.Contains(head, "\r\nBcc:") {
		t.Fatalf("header injection through the title:\n%s", head)
	}
	if !strings.Contains(body, "https://app.example.com/") {
		t.Errorf("the link is missing:\n%s", body)
	}

	srv.rcptCode = "550"
	if err := mailer.Send(ctx, "nobody@example.com", n); !IsPermanent(err) {
		t.Errorf("a 5xx refusal must be permanent, got %v", err)
	}
	if err := mailer.Send(ctx, "not an address", n); !IsPermanent(err) {
		t.Errorf("an invalid address must be permanent, got %v", err)
	}
}

func TestEmailUnreachableRelayIsRetried(t *testing.T) {
	mailer, _ := NewEmail(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "noreply@example.com", Security: "none"})
	err := mailer.Send(context.Background(), "alice@example.com", domain.Notification{Title: "x"})
	if err == nil || IsPermanent(err) {
		t.Fatalf("want a transient error, got %v", err)
	}
}
