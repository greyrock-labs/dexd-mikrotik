// Package plan computes the minimal set of DNS changes needed to bring
// the current DNS records into sync with the desired state derived from
// running containers.
package plan

import (
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/doonga/dexd-mikrotik/internal/dns"
	"github.com/doonga/dexd-mikrotik/internal/registry"
	"github.com/doonga/dexd-mikrotik/internal/source"
)

// Changes holds the buckets of DNS work to perform.
type Changes struct {
	// Create holds endpoints that don't yet have a matching record in the DNS provider.
	Create []*source.Endpoint
	// Update holds endpoints whose record exists but has a different target value.
	Update []*source.Endpoint
	// Replace holds endpoints whose hostname is already owned by us under a
	// different record type and must be replaced before the desired record can be created.
	Replace []Replace
	// Delete holds A/CNAME records owned by us that are no longer in the desired set.
	Delete []dns.Record
	// OrphanTXT holds TXT ownership records we own that have no companion A/CNAME
	// and are no longer desired. They should be deleted to keep the DNS provider clean.
	OrphanTXT []dns.Record
	// Conflicts holds sorted, de-duplicated hostnames whose A/CNAME entries were
	// left untouched. A hostname that is desired or owned by us is a conflict
	// when any of these holds:
	//   - more than one A/CNAME entry exists at the hostname;
	//   - an unowned A/CNAME exists at a desired hostname;
	//   - another writer's ownership TXT exists at either of its ownership keys;
	//   - it is desired as both an A and a CNAME.
	Conflicts []string
}

type Replace struct {
	Old     dns.Record
	Desired *source.Endpoint
}

type recordKey struct {
	Hostname   string
	RecordType string
}

// DesiredRecordCounts returns the number of unique desired A and CNAME records
// by type. Multiple containers claiming the same hostname/type count once, which
// mirrors the collision handling in Compute.
func DesiredRecordCounts(desired []*source.Endpoint) map[string]int {
	counts := map[string]int{"A": 0, "CNAME": 0}
	seen := make(map[recordKey]bool, len(desired))

	for _, ep := range desired {
		if ep.RecordType != "A" && ep.RecordType != "CNAME" {
			continue
		}
		key := recordKey{Hostname: strings.ToLower(ep.DNSName), RecordType: ep.RecordType}
		if seen[key] {
			continue
		}
		seen[key] = true
		counts[ep.RecordType]++
	}

	return counts
}

// Compute diffs desired endpoints against the current DNS records and
// returns what needs to change. It only touches records whose companion
// TXT ownership record matches ownerID; all others are left untouched.
// txtPrefix must match the value used when writing TXT keys (see registry.TXTKey).
//
// The router may be shared with external-dns and hand-made entries, and may
// hold several entries with the same name. Names compare case-insensitively.
// For a hostname that is desired or owned by us, any of the following makes it
// a conflict: more than one A/CNAME entry, another writer's ownership TXT at
// either of its ownership keys, or an unowned A/CNAME at a desired hostname.
// A hostname desired as both A and CNAME is also a conflict.
// Conflicted hostnames are reported in Conflicts and their A/CNAME entries are
// never created, updated, replaced or deleted. Our own orphan TXT at such a
// hostname may still be cleaned up.
func Compute(desired []*source.Endpoint, current []dns.Record, ownerID, txtPrefix string) Changes {
	txtPrefix = strings.ToLower(txtPrefix)

	// Index current A/CNAME entries by lower-cased hostname.
	aOrCnameByHost := make(map[string][]dns.Record)
	var txts []dns.Record
	for _, r := range current {
		switch r.Type {
		case "A", "CNAME":
			host := strings.ToLower(r.Name)
			aOrCnameByHost[host] = append(aOrCnameByHost[host], r)
		case "TXT":
			txts = append(txts, r)
		}
	}

	// Index ownership TXTs. A key is owned if any TXT at it is ours; only that
	// TXT is remembered, so foreign TXTs never reach Delete or OrphanTXT.
	// Another writer's ownership TXT at either key of a hostname is recorded
	// against the hostname.
	ownedTXT := make(map[recordKey]dns.Record)
	foreignHeritage := make(map[string]string) // hostname → foreign heritage
	for _, txt := range txts {
		recordType, hostname, ok := registry.ParseTXTKey(txtPrefix, strings.ToLower(txt.Name))
		// Only our a-/cname- names are ownership keys. Anything else (an
		// external-dns aaaa- key, or a TXT at a dashed hostname such as
		// my-app.example.com) is not about the A/CNAME we manage.
		if !ok || (recordType != "A" && recordType != "CNAME") {
			continue
		}
		key := recordKey{Hostname: hostname, RecordType: recordType}
		if registry.IsOwnedBy(txt.Value, ownerID) {
			if _, seen := ownedTXT[key]; !seen {
				ownedTXT[key] = txt
			}
		} else if heritage, foreign := registry.ForeignHeritage(txt.Value, ownerID); foreign {
			if _, seen := foreignHeritage[hostname]; !seen {
				foreignHeritage[hostname] = heritage
			}
		}
	}

	// Build desired set; detect collisions (same hostname+type from two containers).
	// Upstream callers sort containers by name, so "first" is deterministic.
	desiredByKey := make(map[recordKey]*source.Endpoint, len(desired))
	relevant := make(map[string]bool) // hostnames that are desired or owned
	for key := range ownedTXT {
		relevant[key.Hostname] = true
	}
	for _, ep := range desired {
		normalized := *ep
		normalized.DNSName = strings.ToLower(ep.DNSName)
		key := recordKey{Hostname: normalized.DNSName, RecordType: normalized.RecordType}
		relevant[key.Hostname] = true
		if existing, collision := desiredByKey[key]; collision {
			slog.Warn("hostname collision: two containers claim the same record; keeping first",
				"hostname", normalized.DNSName,
				"record_type", normalized.RecordType,
				"winner", existing.Resource,
				"dropped", normalized.Resource,
			)
			continue
		}
		desiredByKey[key] = &normalized
	}

	// Hostnames with more than one A/CNAME entry are ambiguous: we cannot tell
	// which entry is ours. They are conflicts only when we care about the
	// hostname; otherwise they are someone else's business and are ignored.
	// Either way they never enter aOrCnameByKey, so nothing can touch them.
	conflicts := make(map[string]bool)
	aOrCnameByKey := make(map[recordKey]dns.Record)
	for host, recs := range aOrCnameByHost {
		if len(recs) == 1 {
			aOrCnameByKey[recordKey{Hostname: host, RecordType: recs[0].Type}] = recs[0]
			continue
		}
		if relevant[host] {
			slog.Warn("multiple A/CNAME entries at hostname; skipping until resolved",
				"hostname", host, "count", len(recs))
			conflicts[host] = true
		}
	}

	// A hostname cannot hold both an A and a CNAME. When containers ask for
	// both, there is no correct winner, so touch neither.
	for key, ep := range desiredByKey {
		if key.RecordType != "A" {
			continue
		}
		if cname, both := desiredByKey[recordKey{Hostname: key.Hostname, RecordType: "CNAME"}]; both {
			slog.Warn("hostname desired as both A and CNAME; skipping until resolved",
				"hostname", key.Hostname,
				"a_resource", ep.Resource,
				"cname_resource", cname.Resource,
			)
			conflicts[key.Hostname] = true
		}
	}

	// Another writer's ownership TXT means it believes it owns the A/CNAME at
	// this hostname (external-dns would delete an entry we created there).
	for host, heritage := range foreignHeritage {
		if relevant[host] {
			slog.Warn("foreign ownership TXT at hostname; skipping until resolved",
				"hostname", host, "heritage", heritage)
			conflicts[host] = true
		}
	}

	var changes Changes

	// Determine creates and updates.
	replacedOld := make(map[recordKey]bool)
	for key, ep := range desiredByKey {
		if conflicts[key.Hostname] {
			continue
		}
		existing, exists := aOrCnameByKey[key]
		_, owned := ownedTXT[key]
		switch {
		case !exists:
			if old, ok := ownedOtherType(key, desiredByKey, aOrCnameByKey, ownedTXT); ok {
				changes.Replace = append(changes.Replace, Replace{Old: old, Desired: ep})
				replacedOld[recordKey{Hostname: key.Hostname, RecordType: old.Type}] = true
				continue
			}
			if other, ok := otherTypeRecord(key, aOrCnameByKey); ok {
				slog.Warn("unowned record at desired hostname with different type; skipping until manually resolved",
					"hostname", key.Hostname,
					"current_record_type", other.Type,
					"desired_record_type", key.RecordType,
					"current_value", other.Value,
					"desired_value", ep.Target,
				)
				conflicts[key.Hostname] = true
				continue
			}
			changes.Create = append(changes.Create, ep)
		case !owned:
			// Record exists but we did not create it; warn every reconcile.
			slog.Warn("unowned record at desired hostname; skipping until manually resolved",
				"hostname", key.Hostname,
				"record_type", key.RecordType,
				"current_value", existing.Value,
				"desired_value", ep.Target,
			)
			conflicts[key.Hostname] = true
		case existing.Value != ep.Target:
			changes.Update = append(changes.Update, ep)
		}
	}

	// Determine deletes and orphans among keys we own that are no longer desired.
	for key, txt := range ownedTXT {
		if replacedOld[key] {
			continue
		}
		if _, wanted := desiredByKey[key]; wanted {
			continue
		}
		if !hasType(aOrCnameByHost[key.Hostname], key.RecordType) {
			// No companion entry: only our own TXT remains, safe to remove
			// even at a conflicted hostname.
			changes.OrphanTXT = append(changes.OrphanTXT, txt)
			continue
		}
		if conflicts[key.Hostname] {
			continue
		}
		// Not conflicted, so the hostname holds exactly this one entry.
		if rec, ok := aOrCnameByKey[key]; ok {
			changes.Delete = append(changes.Delete, rec)
		}
	}

	changes.Conflicts = slices.Sorted(maps.Keys(conflicts))
	return changes
}

func ownedOtherType(
	key recordKey,
	desiredByKey map[recordKey]*source.Endpoint,
	aOrCnameByKey map[recordKey]dns.Record,
	ownedTXT map[recordKey]dns.Record,
) (dns.Record, bool) {
	for _, recordType := range []string{"A", "CNAME"} {
		if recordType == key.RecordType {
			continue
		}
		oldKey := recordKey{Hostname: key.Hostname, RecordType: recordType}
		if _, wanted := desiredByKey[oldKey]; wanted {
			continue
		}
		rec, exists := aOrCnameByKey[oldKey]
		if _, owned := ownedTXT[oldKey]; exists && owned {
			return rec, true
		}
	}
	return dns.Record{}, false
}

func otherTypeRecord(key recordKey, aOrCnameByKey map[recordKey]dns.Record) (dns.Record, bool) {
	for _, recordType := range []string{"A", "CNAME"} {
		if recordType == key.RecordType {
			continue
		}
		rec, exists := aOrCnameByKey[recordKey{Hostname: key.Hostname, RecordType: recordType}]
		if exists {
			return rec, true
		}
	}
	return dns.Record{}, false
}

func hasType(recs []dns.Record, recordType string) bool {
	for _, r := range recs {
		if r.Type == recordType {
			return true
		}
	}
	return false
}
