package mikrotik

import (
	"regexp"
	"strings"
)

// DNSRecord is dexd's provider-neutral view of a static DNS entry.
// The client translates it to and from the RouterOS wire format.
type DNSRecord struct {
	ID         string
	Key        string
	RecordType string
	Value      string
}

// wireRecord is the RouterOS REST representation of an /ip/dns/static entry.
// RouterOS returns every property as a string.
type wireRecord struct {
	ID      string `json:".id,omitempty"`
	Name    string `json:"name,omitempty"`
	Regexp  string `json:"regexp,omitempty"`
	Type    string `json:"type,omitempty"`
	Address string `json:"address,omitempty"`
	CName   string `json:"cname,omitempty"`
	Text    string `json:"text,omitempty"`
	TTL     string `json:"ttl,omitempty"`
}

// wildcardPrefix is the only wildcard form dexd supports: a leading "*." label.
const wildcardPrefix = "*."

// wildcardRegexpPrefix and wildcardRegexpSuffix wrap the escaped suffix in
// the regexp dexd writes for "*.<suffix>" names.
const (
	wildcardRegexpPrefix = `^.+\.`
	wildcardRegexpSuffix = `$`
)

// isWildcard reports whether key is a leading-label wildcard such as *.example.com.
func isWildcard(key string) bool {
	return strings.HasPrefix(key, wildcardPrefix)
}

// wildcardToRegexp converts *.example.com to ^.+\.example\.com$.
// RouterOS has no native wildcard names, so wildcards are stored as regexp entries.
func wildcardToRegexp(key string) string {
	suffix := strings.TrimPrefix(key, wildcardPrefix)
	return wildcardRegexpPrefix + regexp.QuoteMeta(suffix) + wildcardRegexpSuffix
}

// regexpToWildcard reverses wildcardToRegexp. It returns false for any regexp
// dexd did not generate, so hand-written regexp entries are never managed.
func regexpToWildcard(re string) (string, bool) {
	escaped, ok := strings.CutPrefix(re, wildcardRegexpPrefix)
	if !ok {
		return "", false
	}
	escaped, ok = strings.CutSuffix(escaped, wildcardRegexpSuffix)
	if !ok || escaped == "" {
		return "", false
	}
	suffix := strings.ReplaceAll(escaped, `\.`, ".")
	if wildcardToRegexp(wildcardPrefix+suffix) != re {
		return "", false
	}
	return wildcardPrefix + suffix, true
}

// toWire builds the RouterOS body for a create.
func toWire(r DNSRecord) wireRecord {
	w := wireRecord{Type: r.RecordType}
	if isWildcard(r.Key) {
		w.Regexp = wildcardToRegexp(r.Key)
	} else {
		w.Name = r.Key
	}
	setWireValue(&w, r.RecordType, r.Value)
	return w
}

func setWireValue(w *wireRecord, recordType, value string) {
	switch recordType {
	case "A", "AAAA":
		w.Address = value
	case "CNAME":
		w.CName = value
	case "TXT":
		w.Text = value
	}
}

// fromWire converts a RouterOS entry into a DNSRecord. It returns false for
// entries dexd cannot manage: unsupported types and foreign regexp entries.
func fromWire(w wireRecord) (DNSRecord, bool) {
	recordType := w.Type
	if recordType == "" {
		// RouterOS may omit type for entries created with the default (A).
		recordType = "A"
		if strings.Contains(w.Address, ":") {
			recordType = "AAAA"
		}
	}

	key := w.Name
	if key == "" {
		wildcard, ok := regexpToWildcard(w.Regexp)
		if !ok {
			return DNSRecord{}, false
		}
		key = wildcard
	}

	r := DNSRecord{ID: w.ID, Key: key, RecordType: recordType}
	switch recordType {
	case "A", "AAAA":
		r.Value = w.Address
	case "CNAME":
		r.Value = w.CName
	case "TXT":
		r.Value = w.Text
	default:
		return DNSRecord{}, false
	}
	return r, true
}
