package redact

import (
	"strings"
	"testing"
)

const subscriptionURL = "https://sub.example.com/api/v1/client/subscribe?token=9f8e7d6c5b4a39281706f5e4d3c2b1a0&flag=meta"

func TestKnownSecretsAreRemovedEverywhere(t *testing.T) {
	r := New(`C:\Users\alice`)
	r.AddSubscriptionURL(subscriptionURL)
	r.Add("controller-secret-123", "控制密钥")
	text := "fetched " + subscriptionURL + " ok\n" +
		"bare token 9f8e7d6c5b4a39281706f5e4d3c2b1a0 leaked in log\n" +
		"Authorization: Bearer controller-secret-123\n" +
		`path C:\Users\alice\AppData\Roaming\MyTizi\settings.json` + "\n" +
		`lower c:\users\ALICE\AppData\Local\MyTizi\Logs\app.log` + "\n" +
		`slashes C:/Users/alice/AppData` + "\n" +
		`json "C:\\Users\\alice\\AppData"`
	got := r.Redact(text)
	for _, leak := range []string{"9f8e7d6c5b4a39281706f5e4d3c2b1a0", "controller-secret-123", "alice", "ALICE"} {
		if strings.Contains(got, leak) {
			t.Errorf("泄漏了 %q：\n%s", leak, got)
		}
	}
	for _, want := range []string{`~\AppData\Roaming\MyTizi\settings.json`, `~\AppData\Local\MyTizi`, "~/AppData", `"~\\AppData"`, "<已脱敏:订阅链接>"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q：\n%s", want, got)
		}
	}
}

func TestUnixHomeIsCaseSensitive(t *testing.T) {
	r := New("/Users/alice")
	got := r.Redact("/Users/alice/Library and /users/alice/x")
	if got != "~/Library and /users/alice/x" {
		t.Fatalf("got %q", got)
	}
}

func TestPatternsCatchUnregisteredSecrets(t *testing.T) {
	r := New(`C:\nonexistent`)
	samples := []string{
		"GET /sub?token=abcdef123456&x=1",
		"password: hunter2hunter2",
		`"uuid": "b831381d-6324-4d53-ad4f-8cda48b30811"`,
		"vmess://eyJ2IjoiMiIsInBzIjoiSlAtMDMifQ==",
		"ss://YWVzLTEyOC1nY206dGVzdHBhc3M@203.0.113.1:8388#HK",
		"trojan://pass123@203.0.113.2:443?sni=example.com#US",
		"Authorization: Bearer abc.def.ghi",
	}
	for _, sample := range samples {
		got := r.Redact(sample)
		for _, s := range []string{"abcdef123456", "hunter2hunter2", "b831381d", "eyJ2IjoiMiIs", "YWVzLTEyOC1n", "pass123@", "abc.def.ghi"} {
			if strings.Contains(got, s) {
				t.Errorf("%s → %s", sample, got)
			}
		}
	}
}

func TestOrdinaryDiagnosticTextSurvives(t *testing.T) {
	r := New(`C:\Users\alice`)
	text := "[TCP] 198.18.0.1:60822 --> www.baidu.com:443 match DomainSuffix(baidu.com) using DIRECT\nnameserver : 223.5.5.5"
	if got := r.Redact(text); got != text {
		t.Fatalf("got %q", got)
	}
}

func TestShortValuesAreNotRegistered(t *testing.T) {
	r := New("/x")
	r.Add("ab", "短")
	if got := r.Redact("abab"); got != "abab" {
		t.Fatalf("got %q", got)
	}
}
