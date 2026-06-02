package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCredentialsJSONRoundTrip(t *testing.T) {
	in := Credentials{
		Access:  "access-tok",
		Refresh: "refresh-tok",
		Expires: 1730000000000,
		Extra: map[string]any{
			"accountId":     "acc_123",
			"enterpriseUrl": "company.ghe.com",
		},
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The object must be flat: extras live alongside access/refresh/expires.
	var flat map[string]any
	if err := json.Unmarshal(data, &flat); err != nil {
		t.Fatalf("unmarshal flat: %v", err)
	}
	for _, k := range []string{"access", "refresh", "expires", "accountId", "enterpriseUrl"} {
		if _, ok := flat[k]; !ok {
			t.Errorf("expected top-level key %q in %s", k, data)
		}
	}

	var out Credentials
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Access != in.Access || out.Refresh != in.Refresh || out.Expires != in.Expires {
		t.Errorf("core fields mismatch: %+v", out)
	}
	if out.StringExtra("accountId") != "acc_123" {
		t.Errorf("accountId extra lost: %q", out.StringExtra("accountId"))
	}
	if out.StringExtra("enterpriseUrl") != "company.ghe.com" {
		t.Errorf("enterpriseUrl extra lost: %q", out.StringExtra("enterpriseUrl"))
	}
}

func TestCredentialsExpired(t *testing.T) {
	setNow(t, 1000)
	if (Credentials{Expires: 999}).Expired() != true {
		t.Errorf("expected expired when now >= expires")
	}
	if (Credentials{Expires: 1000}).Expired() != true {
		t.Errorf("expected expired at exact boundary")
	}
	if (Credentials{Expires: 1001}).Expired() != false {
		t.Errorf("expected not expired when now < expires")
	}
}

func TestStringExtraMissing(t *testing.T) {
	c := Credentials{}
	if c.StringExtra("nope") != "" {
		t.Errorf("expected empty for missing extra")
	}
}

func TestCredentialsUnmarshalNumberExpires(t *testing.T) {
	var c Credentials
	if err := json.Unmarshal([]byte(`{"access":"a","refresh":"r","expires":1730000000000}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Expires != 1730000000000 {
		t.Errorf("expires = %d", c.Expires)
	}
	if !strings.HasPrefix(c.Access, "a") {
		t.Errorf("access = %q", c.Access)
	}
}
