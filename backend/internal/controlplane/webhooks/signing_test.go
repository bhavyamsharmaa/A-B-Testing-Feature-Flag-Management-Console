package webhooks

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The golden vector was computed independently with Node's crypto:
//
//	createHmac("sha256", secret).update(timestamp + "." + deliveryId + "." + body).digest("hex")
//
// If this test fails, the signature scheme changed and every receiver breaks.
const (
	goldenSecret   = "whsec_golden_test_secret_do_not_use"
	goldenSecret2  = "whsec_second_golden_secret"
	goldenTS       = int64(1700000000)
	goldenDelivery = "11111111-2222-3333-4444-555555555555"
	goldenBody     = `{"id":"evt_1","type":"flag.updated","data":{"flag":{"key":"checkout"}}}`
	goldenSig      = "acf5edffd32e8c912dbdb105e4fad4f177f53abc655707b4a87aea5ce817ca1c"
	goldenSig2     = "1b85d217b202470e83832e73eaf7b1e20360b443792ba9ef71bd1220778c5b85"
)

func TestGoldenSignatureVector(t *testing.T) {
	if got := Sign(goldenSecret, goldenTS, goldenDelivery, []byte(goldenBody)); got != goldenSig {
		t.Fatalf("signature changed:\n got  %s\n want %s", got, goldenSig)
	}
	if got := Sign(goldenSecret2, goldenTS, goldenDelivery, []byte(goldenBody)); got != goldenSig2 {
		t.Fatalf("second vector changed: %s", got)
	}
	if h := SignatureHeader([]string{goldenSecret}, goldenTS, goldenDelivery, []byte(goldenBody)); h != "v1="+goldenSig {
		t.Errorf("header %q", h)
	}
	if h := SignatureHeader([]string{goldenSecret, goldenSecret2}, goldenTS, goldenDelivery, []byte(goldenBody)); h != "v1="+goldenSig+",v1="+goldenSig2 {
		t.Errorf("rotation header %q", h)
	}
}

func TestVerifyAcceptsTheRightSignatureWithinTheTolerance(t *testing.T) {
	now := time.Unix(goldenTS, 0)
	hdr := "v1=" + goldenSig
	if err := Verify(goldenSecret, hdr, "1700000000", goldenDelivery, []byte(goldenBody), now, Tolerance); err != nil {
		t.Fatal(err)
	}
	// 4 minutes either way is fine; 6 is not
	for _, d := range []time.Duration{4 * time.Minute, -4 * time.Minute} {
		if err := Verify(goldenSecret, hdr, "1700000000", goldenDelivery, []byte(goldenBody), now.Add(d), Tolerance); err != nil {
			t.Errorf("%v: %v", d, err)
		}
	}
	for _, d := range []time.Duration{6 * time.Minute, -6 * time.Minute, 24 * time.Hour} {
		if err := Verify(goldenSecret, hdr, "1700000000", goldenDelivery, []byte(goldenBody), now.Add(d), Tolerance); !errors.Is(err, ErrBadTimestamp) {
			t.Errorf("%v: %v, want a timestamp refusal (replay window)", d, err)
		}
	}
}

func TestVerifyRefusesAnythingTamperedWith(t *testing.T) {
	now := time.Unix(goldenTS, 0)
	good := "v1=" + goldenSig
	cases := map[string]struct {
		secret, header, ts, delivery, body string
	}{
		"wrong secret":        {goldenSecret2, good, "1700000000", goldenDelivery, goldenBody},
		"changed body":        {goldenSecret, good, "1700000000", goldenDelivery, goldenBody + " "},
		"changed delivery id": {goldenSecret, good, "1700000000", "11111111-2222-3333-4444-555555555556", goldenBody},
		"changed timestamp":   {goldenSecret, good, "1700000001", goldenDelivery, goldenBody},
		"no signature":        {goldenSecret, "", "1700000000", goldenDelivery, goldenBody},
		"wrong scheme":        {goldenSecret, "v2=" + goldenSig, "1700000000", goldenDelivery, goldenBody},
		"truncated":           {goldenSecret, "v1=" + goldenSig[:40], "1700000000", goldenDelivery, goldenBody},
		"no timestamp":        {goldenSecret, good, "", goldenDelivery, goldenBody},
		"junk timestamp":      {goldenSecret, good, "yesterday", goldenDelivery, goldenBody},
	}
	for name, c := range cases {
		if err := Verify(c.secret, c.header, c.ts, c.delivery, []byte(c.body), now, Tolerance); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// During a rotation both signatures are sent: a receiver on either secret verifies.
func TestARotationHeaderVerifiesWithEitherSecret(t *testing.T) {
	now := time.Unix(goldenTS, 0)
	hdr := SignatureHeader([]string{goldenSecret, goldenSecret2}, goldenTS, goldenDelivery, []byte(goldenBody))
	for _, s := range []string{goldenSecret, goldenSecret2} {
		if err := Verify(s, hdr, "1700000000", goldenDelivery, []byte(goldenBody), now, Tolerance); err != nil {
			t.Errorf("secret %s: %v", s[:10], err)
		}
	}
	if err := Verify("whsec_unrelated", hdr, "1700000000", goldenDelivery, []byte(goldenBody), now, Tolerance); err == nil {
		t.Error("an unrelated secret verified")
	}
}

func TestSecretsAreRandomPrefixedAndHinted(t *testing.T) {
	a, _ := NewSecret()
	b, _ := NewSecret()
	if a == b || !strings.HasPrefix(a, SecretPrefix) || len(a) < len(SecretPrefix)+43 {
		t.Errorf("%q %q", a, b)
	}
	if h := SecretHint(a); !strings.HasSuffix(a, strings.TrimPrefix(h, "…")) || len(h) > 8 {
		t.Errorf("hint %q", h)
	}
}

func TestEventTypesAreTheDocumentedNine(t *testing.T) {
	want := []string{"flag.created", "flag.updated", "flag.killed", "flag.deleted", "experiment.started", "experiment.stopped", "promotion.applied", "scheduled_change.executed", "scheduled_change.failed"}
	if strings.Join(Types, ",") != strings.Join(want, ",") {
		t.Errorf("%v", Types)
	}
	if ValidType(EventPing) || ValidType("flag.exploded") || ValidType("") {
		t.Error("an unsubscribable type is valid")
	}
}

func TestOversizedEventsAreShrunkNotRefused(t *testing.T) {
	big := map[string]any{"flag": FlagRef{Key: "k"}, "changes": strings.Repeat("x", MaxEventBytes+10)}
	env := Envelope("evt", Event{Type: EventFlagUpdated, WorkspaceID: "w", Data: big}, time.Unix(0, 0))
	d := env["data"].(map[string]any)
	if d["truncated"] != true || d["flag"] == nil || d["changes"] != nil {
		t.Errorf("%v", d)
	}
}
