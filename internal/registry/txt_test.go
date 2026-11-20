package registry

import "testing"

func TestTXTKey(t *testing.T) {
	cases := []struct {
		prefix, recordType, dnsName, want string
	}{
		{"", "A", "foo.example.com", "a-foo.example.com"},
		{"", "a", "foo.example.com", "a-foo.example.com"},
		{"", "CNAME", "x.y.z", "cname-x.y.z"},
		{"userprefix.", "A", "db.example.com", "userprefix.a-db.example.com"},
		{"userprefix.", "CNAME", "app.example.com", "userprefix.cname-app.example.com"},
	}
	for _, c := range cases {
		if got := TXTKey(c.prefix, c.recordType, c.dnsName); got != c.want {
			t.Errorf("TXTKey(%q, %q, %q) = %q, want %q", c.prefix, c.recordType, c.dnsName, got, c.want)
		}
	}
}

func TestParseTXTKey(t *testing.T) {
	cases := []struct {
		name               string
		prefix, txtKey     string
		wantType, wantHost string
		wantOK             bool
	}{
		{
			name:     "A without prefix",
			txtKey:   "a-foo.example.com",
			wantType: "A",
			wantHost: "foo.example.com",
			wantOK:   true,
		},
		{
			name:     "CNAME without prefix",
			txtKey:   "cname-foo.example.com",
			wantType: "CNAME",
			wantHost: "foo.example.com",
			wantOK:   true,
		},
		{
			name:     "A with prefix",
			prefix:   "talos.",
			txtKey:   "talos.a-foo.example.com",
			wantType: "A",
			wantHost: "foo.example.com",
			wantOK:   true,
		},
		{
			name:     "CNAME with prefix",
			prefix:   "talos.",
			txtKey:   "talos.cname-foo.example.com",
			wantType: "CNAME",
			wantHost: "foo.example.com",
			wantOK:   true,
		},
		{
			name:   "missing prefix",
			prefix: "talos.",
			txtKey: "a-foo.example.com",
		},
		{
			name:   "missing dash",
			txtKey: "afoo.example.com",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotType, gotHost, ok := ParseTXTKey(c.prefix, c.txtKey)
			if ok != c.wantOK {
				t.Fatalf("ParseTXTKey ok=%v, want %v", ok, c.wantOK)
			}
			if gotType != c.wantType || gotHost != c.wantHost {
				t.Errorf("ParseTXTKey = (%q, %q), want (%q, %q)", gotType, gotHost, c.wantType, c.wantHost)
			}
		})
	}
}

func TestEncodeTXT_Unquoted(t *testing.T) {
	got := EncodeTXT("o", "docker/c")
	want := "heritage=dexd-mikrotik,dexd-mikrotik/owner=o,dexd-mikrotik/resource=docker/c"
	if got != want {
		t.Errorf("EncodeTXT = %q, want %q", got, want)
	}
}

func TestDecodeTXT_AcceptsQuotedAndUnquoted(t *testing.T) {
	unquoted := "heritage=dexd-mikrotik,dexd-mikrotik/owner=o,dexd-mikrotik/resource=docker/c"
	want := OwnershipRecord{OwnerID: "o", Resource: "docker/c"}
	for _, v := range []string{unquoted, `"` + unquoted + `"`} {
		got, ok := DecodeTXT(v)
		if !ok {
			t.Errorf("DecodeTXT(%q) ok=false, want true", v)
			continue
		}
		if got != want {
			t.Errorf("DecodeTXT(%q) = %+v, want %+v", v, got, want)
		}
	}
}

func TestDecodeTXT_RejectsForeignHeritage(t *testing.T) {
	cases := []string{
		"heritage=external-dns,external-dns/owner=default,external-dns/resource=ingress/x/y",
		"heritage=dexd,dexd/owner=o,dexd/resource=docker/c",
	}
	for _, c := range cases {
		if _, ok := DecodeTXT(c); ok {
			t.Errorf("DecodeTXT(%q) ok=true, want false", c)
		}
	}
}

func TestDecodeTXT_RoundTrip(t *testing.T) {
	const owner = "dexd"
	const resource = "docker/whoami"

	rec, ok := DecodeTXT(EncodeTXT(owner, resource))
	if !ok {
		t.Fatalf("DecodeTXT(EncodeTXT(...)) failed")
	}
	if rec.OwnerID != owner {
		t.Errorf("OwnerID = %q, want %q", rec.OwnerID, owner)
	}
	if rec.Resource != resource {
		t.Errorf("Resource = %q, want %q", rec.Resource, resource)
	}
}

func TestDecodeTXT_StripsQuotes(t *testing.T) {
	// Even if the value comes back with quotes, it must still parse.
	unquoted := "heritage=dexd-mikrotik,dexd-mikrotik/owner=us,dexd-mikrotik/resource=docker/x"
	if _, ok := DecodeTXT(unquoted); !ok {
		t.Errorf("DecodeTXT failed on unquoted value")
	}

	quoted := `"` + unquoted + `"`
	rec, ok := DecodeTXT(quoted)
	if !ok {
		t.Fatalf("DecodeTXT failed on quoted value")
	}
	if rec.OwnerID != "us" {
		t.Errorf("OwnerID = %q, want %q", rec.OwnerID, "us")
	}
}

func TestDecodeTXT_RejectsInvalidHeritage(t *testing.T) {
	cases := []string{
		"",
		"random text",
		"v=spf1 include:_spf.google.com ~all",
		`"some other quoted thing"`,
		"heritage=another-agent,dexd-mikrotik/owner=us",
	}
	for _, c := range cases {
		if _, ok := DecodeTXT(c); ok {
			t.Errorf("DecodeTXT(%q) returned ok=true, want false", c)
		}
	}
}

func TestDecodeTXT_RequiresOwner(t *testing.T) {
	// Heritage present but no owner field — should reject.
	if _, ok := DecodeTXT("heritage=dexd-mikrotik,dexd-mikrotik/resource=foo"); ok {
		t.Error("DecodeTXT must reject values without an owner field")
	}
}

func TestIsOwnedBy(t *testing.T) {
	encoded := EncodeTXT("us", "docker/x")

	if !IsOwnedBy(encoded, "us") {
		t.Error("IsOwnedBy returned false for matching owner")
	}
	if IsOwnedBy(encoded, "them") {
		t.Error("IsOwnedBy returned true for non-matching owner")
	}
	if IsOwnedBy("not-an-ownership-record", "us") {
		t.Error("IsOwnedBy returned true for non-heritage value")
	}
}

func TestForeignHeritage(t *testing.T) {
	cases := []struct {
		value, wantHeritage string
		wantForeign         bool
	}{
		{"heritage=external-dns,external-dns/owner=default,external-dns/resource=ingress/x/y", "external-dns", true},
		{`"heritage=external-dns,external-dns/owner=default"`, "external-dns", true},
		{EncodeTXT("other", "docker/c"), "dexd-mikrotik", true},
		{EncodeTXT("us", "docker/c"), "", false},
		{`"` + EncodeTXT("us", "docker/c") + `"`, "", false},
		{"v=spf1 include:_spf.example.com ~all", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		heritage, foreign := ForeignHeritage(c.value, "us")
		if foreign != c.wantForeign || heritage != c.wantHeritage {
			t.Errorf("ForeignHeritage(%q) = (%q, %v), want (%q, %v)",
				c.value, heritage, foreign, c.wantHeritage, c.wantForeign)
		}
	}
}
