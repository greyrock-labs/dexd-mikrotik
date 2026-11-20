// Package dns holds provider-neutral DNS record types.
package dns

// Record is a single DNS record as seen by a provider.
type Record struct {
	ID    string // RouterOS ".id", e.g. "*1A"
	Name  string // lower-cased FQDN
	Type  string // "A", "CNAME" or "TXT"
	Value string // address, cname target, or TXT text
	TTL   int    // seconds; 0 = router default. Only used on create.
}
