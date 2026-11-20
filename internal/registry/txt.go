// Package registry implements dexd-mikrotik TXT ownership tracking.
// For every A record we manage at foo.example.com, we maintain a companion TXT
// record at a-foo.example.com with value:
//
//	heritage=dexd-mikrotik,dexd-mikrotik/owner=<ownerID>,dexd-mikrotik/resource=<resource>
//
// Other writers (external-dns, hand-made entries) may keep their own TXT
// records at the same name; only values with our heritage are ours.
package registry

import (
	"fmt"
	"strings"
)

const (
	heritage         = "dexd-mikrotik"
	ownerField       = "dexd-mikrotik/owner"
	resourceField    = "dexd-mikrotik/resource"
	wildcardTXTLabel = "wildcard-dexd-mikrotik"
	wildcardDNSLabel = "*"
)

// OwnershipRecord holds the parsed payload of a TXT ownership record.
type OwnershipRecord struct {
	OwnerID  string
	Resource string
}

// TXTKey returns the TXT record name for the given DNS name and record type.
// An optional prefix is prepended verbatim (include a trailing separator if needed).
// e.g. TXTKey("", "A", "foo.example.com") == "a-foo.example.com"
// e.g. TXTKey("talos.", "A", "foo.example.com") == "talos.a-foo.example.com"
func TXTKey(prefix, recordType, dnsName string) string {
	return prefix + strings.ToLower(recordType) + "-" + encodeTXTKeyHostname(dnsName)
}

// HostnameFromTXTKey reverses TXTKey: given the same prefix and record type, it
// strips the leading "prefix+recordType-" from txtKey and returns the original
// hostname. Returns ("", false) if txtKey does not carry the expected prefix.
func HostnameFromTXTKey(prefix, recordType, txtKey string) (string, bool) {
	expected := prefix + strings.ToLower(recordType) + "-"
	hostname, ok := strings.CutPrefix(txtKey, expected)
	if !ok {
		return "", false
	}
	return decodeTXTKeyHostname(hostname), true
}

// ParseTXTKey strips prefix from txtKey, then splits on the first '-' to
// recover the upper-case record type and hostname.
func ParseTXTKey(prefix, txtKey string) (recordType, hostname string, ok bool) {
	rest, ok := strings.CutPrefix(txtKey, prefix)
	if !ok {
		return "", "", false
	}
	recordType, hostname, ok = strings.Cut(rest, "-")
	if !ok || recordType == "" || hostname == "" {
		return "", "", false
	}
	return strings.ToUpper(recordType), decodeTXTKeyHostname(hostname), true
}

func encodeTXTKeyHostname(hostname string) string {
	labels := strings.Split(hostname, ".")
	for i, label := range labels {
		if label == wildcardDNSLabel {
			labels[i] = wildcardTXTLabel
		}
	}
	return strings.Join(labels, ".")
}

func decodeTXTKeyHostname(hostname string) string {
	labels := strings.Split(hostname, ".")
	for i, label := range labels {
		if label == wildcardTXTLabel {
			labels[i] = wildcardDNSLabel
		}
	}
	return strings.Join(labels, ".")
}

// EncodeTXT produces the unquoted TXT record value for an ownership record.
// RouterOS stores the text verbatim, so no quoting is added.
func EncodeTXT(ownerID, resource string) string {
	return fmt.Sprintf("heritage=%s,%s=%s,%s=%s",
		heritage, ownerField, ownerID, resourceField, resource)
}

// DecodeTXT parses the TXT record value. Returns (record, true) if it is a
// valid dexd-mikrotik ownership record, (zero, false) otherwise. Quoted and
// unquoted values are both accepted.
func DecodeTXT(value string) (OwnershipRecord, bool) {
	// Strip surrounding quotes that providers may add.
	value = strings.Trim(value, `"`)

	var rec OwnershipRecord
	validHeritage := false
	for part := range strings.SplitSeq(value, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "heritage":
			validHeritage = v == heritage
		case ownerField:
			rec.OwnerID = v
		case resourceField:
			rec.Resource = v
		}
	}
	if !validHeritage || rec.OwnerID == "" {
		return OwnershipRecord{}, false
	}
	return rec, true
}

// IsOwnedBy reports whether the TXT value was written by the given ownerID.
func IsOwnedBy(txtValue, ownerID string) bool {
	rec, ok := DecodeTXT(txtValue)
	return ok && rec.OwnerID == ownerID
}

// ForeignHeritage reports whether txtValue is an ownership TXT written by
// someone else: it carries a heritage= marker but is not ours (another
// heritage such as external-dns, or dexd-mikrotik with a different owner).
// It returns the heritage found. Values without heritage= are not ownership
// records and report false.
func ForeignHeritage(txtValue, ownerID string) (string, bool) {
	value := strings.Trim(txtValue, `"`)
	if !strings.Contains(value, "heritage=") || IsOwnedBy(value, ownerID) {
		return "", false
	}
	for part := range strings.SplitSeq(value, ",") {
		if k, v, ok := strings.Cut(part, "="); ok && k == "heritage" {
			return v, true
		}
	}
	return "", true
}
