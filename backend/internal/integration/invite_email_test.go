package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/delivery"
	"helios/backend/internal/platform/mail"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

type mailWorld struct {
	*tenants
	clock *liveClock
}

func newMailWorld(t *testing.T) *mailWorld {
	t.Helper()
	clock := &liveClock{}
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Clock = clock.now
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{ratelimit.ClassInvite: {PerSecond: 1000, Burst: 1000}}})
	})
	return &mailWorld{tenants: x, clock: clock}
}

type inviteOut struct {
	Invite struct {
		ID, Email, EmailStatus string
	}
	Token string
}

func (w *mailWorld) invite(t *testing.T, email string) inviteOut {
	t.Helper()
	return decode[inviteOut](t, w.h.expect(201, w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites", map[string]any{"email": email, "role": "editor"}))
}

func (w *mailWorld) deliver(t *testing.T) outbox.Stats {
	t.Helper()
	st, err := w.h.deliveryWorker(w.clock.now, outbox.Config{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

type listedInvite struct {
	ID, Email, EmailStatus, EmailError string
}

func (w *mailWorld) list(t *testing.T) ([]listedInvite, struct {
	Mode string
	Sent bool
}) {
	t.Helper()
	r := decode[struct {
		Invites []listedInvite
		Email   struct {
			Mode string
			Sent bool
		}
	}](t, w.h.expect(200, w.a, "GET", "/workspaces/"+w.wsA.ID+"/invites", nil))
	return r.Invites, r.Email
}

func (w *mailWorld) payloadHasToken(inviteID string) bool {
	var has bool
	_ = w.h.pool.QueryRow(context.Background(), `SELECT bool_or(payload ? 'token') FROM outbox WHERE invite_id = $1::uuid`, inviteID).Scan(&has)
	return has
}

func TestInviteEmailIsQueuedInTheSameTransactionAndSentOnce(t *testing.T) {
	w := newMailWorld(t)
	inv := w.invite(t, "newhire@example.com")
	if inv.Invite.EmailStatus != "queued" || inv.Token == "" {
		t.Fatalf("%+v", inv)
	}
	if n := w.h.count(`SELECT count(*) FROM outbox WHERE invite_id = $1::uuid AND kind = 'email' AND status = 'pending'`, inv.Invite.ID); n != 1 {
		t.Fatalf("%d email rows", n)
	}
	// the row holds the token while it waits
	if !w.payloadHasToken(inv.Invite.ID) {
		t.Fatal("the pending payload has no token to send")
	}
	if got, _ := w.list(t); len(got) != 1 || got[0].EmailStatus != "queued" {
		t.Errorf("status before sending: %+v", got)
	}
	if st := w.deliver(t); st.Delivered != 1 {
		t.Fatalf("%+v", st)
	}
	sent := w.h.mail.sent()
	if len(sent) != 1 || sent[0].To != "newhire@example.com" {
		t.Fatalf("sent %+v", sent)
	}
	link := "https://console.example.test/invite/" + inv.Token
	if !strings.Contains(sent[0].Text, link) || !strings.Contains(sent[0].HTML, link) {
		t.Errorf("the email does not carry the invite link")
	}
	for _, want := range []string{w.wsA.Name, w.a.email, "an editor"} {
		if !strings.Contains(sent[0].Text, want) {
			t.Errorf("the email lacks %q", want)
		}
	}
	// delivered: the token is gone from the outbox row
	if w.payloadHasToken(inv.Invite.ID) {
		t.Error("the token is still in the outbox payload after delivery")
	}
	if got, _ := w.list(t); got[0].EmailStatus != "sent" {
		t.Errorf("status after sending: %+v", got)
	}
	// and the link works exactly once, for that address, as before
	invitee := w.h.newUser("newhire")
	w.h.me(invitee)
	w.h.expect(200, invitee, "POST", "/invites/accept", map[string]any{"token": inv.Token})
	if st := w.deliver(t); st.Claimed != 0 || len(w.h.mail.sent()) != 1 {
		t.Errorf("a second delivery pass sent again: %+v", st)
	}
}

func TestAnInviteThatDoesNotCommitQueuesNoEmail(t *testing.T) {
	w := newMailWorld(t)
	// the owner is already a member: 409, no invite, no email
	if st, _ := w.h.call(w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites", map[string]any{"email": w.a.email, "role": "editor"}); st != 409 {
		t.Fatalf("%d", st)
	}
	// an invalid role and a bad address likewise
	w.h.call(w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites", map[string]any{"email": "nope", "role": "editor"})
	w.h.call(w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites", map[string]any{"email": "x@example.com", "role": "owner"})
	if n := w.h.count(`SELECT count(*) FROM outbox WHERE kind = 'email'`); n != 0 {
		t.Errorf("%d email rows after refused invites", n)
	}
}

func TestADeadInviteEmailLosesItsTokenAndSaysFailed(t *testing.T) {
	w := newMailWorld(t)
	inv := w.invite(t, "bounce@example.com")
	w.h.mail.setErr(fmt.Errorf("%w: the SMTP server rejected the recipient", mail.ErrInvalid))
	st := w.deliver(t)
	if st.Dead != 1 {
		t.Fatalf("%+v", st)
	}
	if w.payloadHasToken(inv.Invite.ID) {
		t.Error("the token survives in a dead row")
	}
	if got, _ := w.list(t); got[0].EmailStatus != "failed" || got[0].EmailError == "" {
		t.Errorf("%+v", got)
	}
	// a transient failure retries and keeps the token until it is delivered or dead
	inv2 := w.invite(t, "slow@example.com")
	w.h.mail.setErr(fmt.Errorf("mail: could not connect to the SMTP server"))
	if st := w.deliver(t); st.Retrying != 1 {
		t.Fatalf("%+v", st)
	}
	if !w.payloadHasToken(inv2.Invite.ID) {
		t.Error("the token was scrubbed while a retry is still coming")
	}
	if got, _ := w.list(t); got[0].EmailStatus != "queued" {
		t.Errorf("a retrying email reads %q, want queued", got[0].EmailStatus)
	}
	w.h.mail.setErr(nil)
	w.clock.advance(2 * time.Minute)
	if st := w.deliver(t); st.Delivered != 1 {
		t.Fatalf("%+v", st)
	}
	if w.payloadHasToken(inv2.Invite.ID) {
		t.Error("the token survives after the retry delivered")
	}
}

func TestARevokedOrAcceptedInviteIsNotEmailed(t *testing.T) {
	w := newMailWorld(t)
	revoked := w.invite(t, "revoked@example.com")
	w.h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/invites/"+revoked.Invite.ID, nil)
	accepted := w.invite(t, "accepted@example.com")
	u := w.h.newUser("accepted")
	w.h.me(u)
	w.h.expect(200, u, "POST", "/invites/accept", map[string]any{"token": accepted.Token})
	st := w.deliver(t)
	if st.Dead != 2 || len(w.h.mail.sent()) != 0 {
		t.Fatalf("%+v sent %d", st, len(w.h.mail.sent()))
	}
	for _, id := range []string{revoked.Invite.ID, accepted.Invite.ID} {
		if w.payloadHasToken(id) {
			t.Errorf("invite %s: the token survives in a row that will never be sent", id)
		}
	}
}

func TestResendIssuesANewLinkAndRetiresTheOldOne(t *testing.T) {
	w := newMailWorld(t)
	inv := w.invite(t, "again@example.com")
	w.deliver(t)
	// within the cooldown: refused with Retry-After, nothing changes
	st, hdr, b := w.h.callHdr(w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites/"+inv.Invite.ID+"/resend", nil)
	if st != 429 || hdr.Get("Retry-After") == "" || !bytes.Contains(b, []byte("RESEND_COOLDOWN")) {
		t.Fatalf("resend in the cooldown: %d %s", st, b)
	}
	w.clock.advance(11 * time.Minute)
	res := decode[inviteOut](t, w.h.expect(200, w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites/"+inv.Invite.ID+"/resend", nil))
	if res.Token == "" || res.Token == inv.Token || res.Invite.EmailStatus != "queued" {
		t.Fatalf("%+v", res)
	}
	w.deliver(t)
	sent := w.h.mail.sent()
	if len(sent) != 2 || !strings.Contains(sent[1].Text, res.Token) || strings.Contains(sent[1].Text, inv.Token) {
		t.Fatalf("second email: %+v", sent)
	}
	// the first link is dead, the second works
	u := w.h.newUser("again")
	w.h.me(u)
	w.h.expect(404, u, "POST", "/invites/accept", map[string]any{"token": inv.Token})
	w.h.expect(200, u, "POST", "/invites/accept", map[string]any{"token": res.Token})
	if w.payloadHasToken(inv.Invite.ID) {
		t.Error("a token survives in the outbox")
	}
}

func TestAnEmailStillQueuedWhenAResendHappensIsRetiredNotSentWithTheOldToken(t *testing.T) {
	w := newMailWorld(t)
	inv := w.invite(t, "queued@example.com")
	w.clock.advance(11 * time.Minute)
	res := decode[inviteOut](t, w.h.expect(200, w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites/"+inv.Invite.ID+"/resend", nil))
	w.deliver(t)
	sent := w.h.mail.sent()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, res.Token) || strings.Contains(sent[0].Text, inv.Token) {
		t.Fatalf("sent %+v", sent)
	}
}

func TestInviteEmailLimits(t *testing.T) {
	w := newMailWorld(t)
	// per recipient in a workspace: 3 a day (the invite itself is the first)
	inv := w.invite(t, "popular@example.com")
	for i := 0; i < 2; i++ {
		w.clock.advance(11 * time.Minute)
		w.h.expect(200, w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites/"+inv.Invite.ID+"/resend", nil)
	}
	w.clock.advance(11 * time.Minute)
	st, b := w.h.call(w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites/"+inv.Invite.ID+"/resend", nil)
	if st != 409 || !bytes.Contains(b, []byte("QUOTA_EXCEEDED")) {
		t.Errorf("the 4th email to one address in a day: %d %s", st, b)
	}
	// the day passes
	w.clock.advance(25 * time.Hour)
	w.h.expect(200, w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites/"+inv.Invite.ID+"/resend", nil)

	// per workspace: 50 a day
	w2 := newMailWorld(t)
	first := w2.invite(t, "first@example.com")
	if _, err := w2.h.pool.Exec(context.Background(), `
		INSERT INTO outbox (workspace_id, kind, event_type, event_id, invite_id, target_key, payload, status)
		SELECT $1::uuid, 'email', 'invite.email', gen_random_uuid(), $2::uuid, 'filler' || g || '@example.com', '{}'::jsonb, 'dead' FROM generate_series(1, 49) g`, w2.wsA.ID, first.Invite.ID); err != nil {
		t.Fatal(err)
	}
	st, b = w2.h.call(w2.a, "POST", "/workspaces/"+w2.wsA.ID+"/invites", map[string]any{"email": "one-too-many@example.com", "role": "viewer"})
	if st != 409 || !bytes.Contains(b, []byte("QUOTA_EXCEEDED")) {
		t.Errorf("the 51st invite email in a day: %d %s", st, b)
	}
	if n := w2.h.count(`SELECT count(*) FROM workspace_invites WHERE email = 'one-too-many@example.com'`); n != 0 {
		t.Errorf("a refused invite was stored")
	}
}

// The per-address limit ACROSS workspaces counts other workspaces' activity, so it must not be
// visible as an error: the invite is made, the response is the same, and the status says honestly that
// no email went out.
func TestThePerAddressLimitAcrossWorkspacesIsSilentToTheCaller(t *testing.T) {
	w := newMailWorld(t)
	// B invites the address six times legitimately (via its own API), then A invites it once
	for i := 0; i < 6; i++ {
		bw := &mailWorld{tenants: w.tenants, clock: w.clock}
		inv := decode[inviteOut](t, w.h.expect(201, w.b, "POST", "/workspaces/"+w.wsB.ID+"/invites", map[string]any{"email": fmt.Sprintf("t%d@example.com", i), "role": "viewer"}))
		_, _ = bw, inv
		if _, err := w.h.pool.Exec(context.Background(), `UPDATE outbox SET target_key = 'target@example.com' WHERE invite_id = $1::uuid`, inv.Invite.ID); err != nil {
			t.Fatal(err)
		}
	}
	inv := w.invite(t, "target@example.com")
	if inv.Invite.EmailStatus != "queued" || inv.Token == "" {
		t.Errorf("the response differs when the address is over its cross-workspace limit: %+v", inv)
	}
	w.deliver(t)
	for _, m := range w.h.mail.sent() {
		if m.To == "target@example.com" {
			t.Errorf("an email was sent past the cross-workspace limit")
		}
	}
	list, _ := w.list(t)
	if len(list) != 1 || list[0].EmailStatus != "failed" || !strings.Contains(list[0].EmailError, "too many") {
		t.Errorf("the inviter is not told the truth: %+v", list)
	}
	if w.payloadHasToken(inv.Invite.ID) {
		t.Error("a suppressed email kept a token in its payload")
	}
}

// Inviting an address that has an account must look exactly like inviting one that does not.
func TestInvitingAnExistingAccountLooksTheSameAsAnUnknownAddress(t *testing.T) {
	w := newMailWorld(t)
	existing := w.h.newUser("hasaccount")
	w.h.me(existing) // a real account with a workspace of its own
	shape := func(email string) (int, string, time.Duration) {
		start := time.Now()
		st, b := w.h.call(w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites", map[string]any{"email": email, "role": "viewer"})
		took := time.Since(start)
		// reduce the body to its shape: keys and value kinds, not the per-invite ids and tokens
		s := string(b)
		for _, v := range []string{email} {
			s = strings.ReplaceAll(s, v, "<email>")
		}
		return st, redactIDs(s), took
	}
	var withAccount, without []time.Duration
	var shapeA, shapeB string
	for i := 0; i < 12; i++ {
		stA, bodyA, dA := shape(fmt.Sprintf("hasaccount+%d@example.com", i))
		if i == 0 {
			stA, bodyA, dA = shape(existing.email)
		}
		stB, bodyB, dB := shape(fmt.Sprintf("stranger%d@example.com", i))
		if stA != stB || (i == 0 && bodyA != bodyB) {
			t.Fatalf("status/body differ: %d %s vs %d %s", stA, bodyA, stB, bodyB)
		}
		withAccount, without = append(withAccount, dA), append(without, dB)
		shapeA, shapeB = bodyA, bodyB
	}
	_, _ = shapeA, shapeB
	med := func(d []time.Duration) time.Duration {
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		return d[len(d)/2]
	}
	a, b := med(withAccount), med(without)
	if diff := a - b; diff > 25*time.Millisecond || diff < -25*time.Millisecond {
		t.Errorf("median response time differs by %v (%v vs %v): the response time reveals whether the address has an account", diff, a, b)
	}
	// resend responds the same either way too
	w.clock.advance(11 * time.Minute)
	l, _ := w.list(t)
	for _, inv := range l[:2] {
		if st, _ := w.h.call(w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites/"+inv.ID+"/resend", nil); st != 200 {
			t.Errorf("resend: %d", st)
		}
	}
}

func redactIDs(s string) string {
	out := []rune(s)
	// replace uuids and tokens crudely: runs of [0-9a-zA-Z_-] of length >= 20
	var b strings.Builder
	run := 0
	start := 0
	flush := func(end int) {
		if run >= 20 {
			b.WriteString("<id>")
		} else {
			b.WriteString(string(out[start:end]))
		}
	}
	for i, r := range out {
		ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if ok {
			if run == 0 {
				start = i
			}
			run++
			continue
		}
		if run > 0 {
			flush(i)
			run = 0
		}
		b.WriteRune(r)
	}
	if run > 0 {
		flush(len(out))
	}
	return regexp.MustCompile(`\d{4}-\d{2}-\d{2}T[^"]*`).ReplaceAllString(b.String(), "<time>") // timestamps vary
}

func TestTheSinkIsTheDefaultAndTheConsoleIsToldSo(t *testing.T) {
	x := newTenantsWith(t, func(d *server.Deps) { d.Delivery.Mail = nil }) // what a server with no mail configured has
	l := decode[struct {
		Email struct {
			Mode string
			Sent bool
		}
	}](t, x.h.expect(200, x.a, "GET", "/workspaces/"+x.wsA.ID+"/invites", nil))
	if l.Email.Mode != "sink" || l.Email.Sent {
		t.Errorf("mail mode %+v: a server with no mail settings must use the sink", l.Email)
	}
	w := &mailWorld{tenants: x, clock: &liveClock{}}
	inv := w.invite(t, "devperson@example.com")
	// delivering through the real default (the sink): succeeds, sends nothing anywhere
	o := x.h.deps.Delivery
	o.Mail = &mail.Sink{Logf: func(string, ...any) {}}
	if st := w.deliverWith(t, o); st.Delivered != 1 {
		t.Errorf("%+v", st)
	}
	if w.payloadHasToken(inv.Invite.ID) {
		t.Error("the token remains after the sink 'delivered'")
	}
}

func TestTheInviteTokenNeverReachesTheLogs(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)
	w := newMailWorld(t)
	inv := w.invite(t, "logs@example.com")
	w.h.mail.setErr(fmt.Errorf("mail: could not connect to the SMTP server"))
	w.deliver(t)
	w.h.mail.setErr(nil)
	w.clock.advance(2 * time.Minute)
	w.deliver(t)
	w.clock.advance(11 * time.Minute)
	res := decode[inviteOut](t, w.h.expect(200, w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites/"+inv.Invite.ID+"/resend", nil))
	w.deliver(t)
	// the sink path too
	sink := &mail.Sink{}
	_ = sink.Send(context.Background(), mail.Message{To: "logs@example.com", Subject: "s", Text: "https://c.example/invite/" + inv.Token})
	out := buf.String()
	for _, secret := range []string{inv.Token, res.Token, strings.TrimPrefix(inv.Token, "hinv_"), strings.TrimPrefix(res.Token, "hinv_"), "logs@example.com"} {
		if strings.Contains(out, secret) {
			t.Errorf("the logs contain %q", secret)
		}
	}
}

func (w *mailWorld) deliverWith(t *testing.T, o delivery.Options) outbox.Stats {
	t.Helper()
	o.Outbox = outbox.Config{Now: w.clock.now, Jitter: func() float64 { return 1 }}
	st, err := delivery.NewWorker(w.h.pool, o).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}
