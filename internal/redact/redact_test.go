package redact

import (
	"encoding/json"
	"testing"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
)

func TestLine(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no secret", "ORCH job 4211 started", "ORCH job 4211 started"},
		{"amqp url", "dial amqp://automata:s3cret@rabbitmq:5672/ failed", "dial amqp://***@rabbitmq:5672/ failed"},
		{"http url with user info", `Get "http://user:pw@localhost:8080/api": refused`, `Get "http://***@localhost:8080/api": refused`},
		{"url already redacted", "http://***@localhost:8080", "http://***@localhost:8080"},
		{"url without user info", "http://localhost:8080/a@b", "http://localhost:8080/a@b"},
		{"bearer header", "Authorization: Bearer eyJhbGci.x.y", "Authorization: Bearer ***"},
		{"basic header", "authorization=Basic dXNlcjpwYXNz rest", "authorization=Basic *** rest"},
		{"bare authorization", "Authorization: abc123", "Authorization: ***"},
		{"cookie header", "Cookie: session=abc; theme=dark", "Cookie: ***"},
		{"set-cookie", "Set-Cookie: id=a3fWa; Secure", "Set-Cookie: ***"},
		{"api key header", "X-Api-Key: k-123, next", "X-Api-Key: ***, next"},
		{"env style token", "TOKEN=abc PASSWORD=hunter2", "TOKEN=*** PASSWORD=***"},
		{"json password", `{"user":"a","password":"x y"}`, `{"user":"a","password":***}`},
		{"secret with spaces", "secret : 'a b c'", "secret : ***"},
		{"curl header single quotes", `ffuf -H 'Authorization: Bearer abc def' -u http://t/FUZZ`, `ffuf -H 'Authorization: ***' -u http://t/FUZZ`},
		{"curl header double quotes", `ffuf -H "Cookie: a=1; b=2" -u x`, `ffuf -H "Cookie: ***" -u x`},
		{"long header flag", `--header='X-Api-Key: k1'`, `--header='X-Api-Key: ***'`},
		{"header flag without quotes", `ffuf -H Authorization:abc -u x`, `ffuf -H Authorization: *** -u x`},
		{"harmless header flag", `ffuf -H 'Accept: */*' -u x`, `ffuf -H 'Accept: */*' -u x`},
		{"cookie flag single quotes", `ffuf -b 'session=abc; x=1' -u x`, `ffuf -b '***' -u x`},
		{"cookie flag double quotes", `ffuf --cookie "session=abc" -u x`, `ffuf --cookie "***" -u x`},
		{"cookie flag without quotes", `ffuf -b session=abc -u x`, `ffuf -b *** -u x`},
		{"multibyte text is kept", "worker é ü — token=ä€", "worker é ü — token=***"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Line(c.in); got != c.want {
				t.Errorf("Line(%q)\n got  %q\n want %q", c.in, got, c.want)
			}
		})
	}
}

// Redacting twice must give the same text: the workers control plane redacts again what the worker machine
// controller already redacted.
func TestLineIsIdempotent(t *testing.T) {
	for _, in := range []string{
		`ffuf -H 'Authorization: Bearer abc' -b 'c=1' amqp://u:p@h`,
		"Cookie: a=1; b=2",
		`{"password":"x","token":"y"}`,
	} {
		once := Line(in)
		if twice := Line(once); twice != once {
			t.Errorf("Line(Line(%q)) = %q, want %q", in, twice, once)
		}
	}
}

// The error texts of the contract samples were sanitized by the worker, so the rules must leave them as they are.
func TestSampleErrorsAreAlreadyRedacted(t *testing.T) {
	for _, name := range []string{"worker-status.v1.running.json", "worker-status.v1.idle.json",
		"worker-machine-controller-snapshot.sample.json"} {
		var doc any
		if err := json.Unmarshal(contracttest.Read(t, name), &doc); err != nil {
			t.Fatal(err)
		}
		for _, s := range lastErrors(doc) {
			if got := Line(s); got != s {
				t.Errorf("%s: Line(%q) = %q, want it unchanged", name, s, got)
			}
		}
	}
}

// lastErrors collects every non-empty "lastError" string of a JSON document.
func lastErrors(v any) []string {
	var out []string
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if s, ok := x.(string); ok && k == "lastError" && s != "" {
				out = append(out, s)
			}
			out = append(out, lastErrors(x)...)
		}
	case []any:
		for _, x := range v {
			out = append(out, lastErrors(x)...)
		}
	}
	return out
}

func TestLastErrorsFindsNestedValues(t *testing.T) {
	doc := map[string]any{"lastError": "a", "x": []any{map[string]any{"lastError": "b"}, map[string]any{"lastError": ""}}}
	got := lastErrors(doc)
	if len(got) != 2 {
		t.Errorf("lastErrors = %v, want [a b] in any order", got)
	}
}
