package dns

import (
	"net/netip"
	"sort"
	"time"
)

type CacheClock func() time.Time

type CachedResult struct {
	Domain        string          `json:"domain"`
	IPs           []string        `json:"ips"`
	Outcome       CompiledOutcome `json:"outcome"`
	CachedAt      time.Time       `json:"cached_at"`
	TTL           time.Duration   `json:"ttl"`
	ExpiresAt     time.Time       `json:"expires_at"`
	Expired       bool            `json:"expired"`
	Stale         bool            `json:"stale"`
	ProxyEgressID string          `json:"proxy_egress_id,omitempty"`
}

type DNSCache struct {
	clock   CacheClock
	entries map[string]CachedResult
}

type DomainIPSetEntry struct {
	Domain        string          `json:"domain"`
	IP            string          `json:"ip"`
	Outcome       CompiledOutcome `json:"outcome"`
	CachedAt      time.Time       `json:"cached_at"`
	TTL           time.Duration   `json:"ttl"`
	ExpiresAt     time.Time       `json:"expires_at"`
	Expired       bool            `json:"expired"`
	Stale         bool            `json:"stale"`
	ProxyEgressID string          `json:"proxy_egress_id,omitempty"`
}

type DomainIPSetSnapshot struct {
	Entries []DomainIPSetEntry `json:"entries"`
}

type DomainIPSet struct {
	clock   CacheClock
	entries map[string]DomainIPSetEntry
}

func NewDNSCache(clock CacheClock) *DNSCache {
	if clock == nil {
		clock = time.Now
	}
	return &DNSCache{clock: clock, entries: make(map[string]CachedResult)}
}

func NewDomainIPSet(clock CacheClock) *DomainIPSet {
	if clock == nil {
		clock = time.Now
	}
	return &DomainIPSet{clock: clock, entries: make(map[string]DomainIPSetEntry)}
}

func (cache *DNSCache) Store(domain string, ips []string, outcome CompiledOutcome, ttl time.Duration) CachedResult {
	now := cache.clock()
	result := CachedResult{
		Domain:        domain,
		IPs:           append([]string(nil), ips...),
		Outcome:       outcome,
		CachedAt:      now,
		TTL:           ttl,
		ExpiresAt:     now.Add(ttl),
		ProxyEgressID: outcome.ProxyEgressID,
	}
	if ttl <= 0 {
		result.Expired = true
		result.Stale = true
	}
	cache.entries[domain] = result
	return result
}

func (cache *DNSCache) Lookup(domain string) (CachedResult, bool) {
	result, exists := cache.entries[domain]
	if !exists {
		return CachedResult{}, false
	}
	if cache.isExpired(result) {
		result.Expired = true
		result.Stale = true
		cache.entries[domain] = result
		return CachedResult{}, false
	}
	return cloneCachedResult(result), true
}

func (cache *DNSCache) Stale(domain string) (CachedResult, bool) {
	result, exists := cache.entries[domain]
	if !exists || !cache.isExpired(result) {
		return CachedResult{}, false
	}
	result.Expired = true
	result.Stale = true
	cache.entries[domain] = result
	return cloneCachedResult(result), true
}

func (cache *DNSCache) isExpired(result CachedResult) bool {
	return !cache.clock().Before(result.ExpiresAt)
}

func cloneCachedResult(result CachedResult) CachedResult {
	result.IPs = append([]string(nil), result.IPs...)
	return result
}

func (set *DomainIPSet) StoreResult(result CachedResult) []DomainIPSetEntry {
	entries := make([]DomainIPSetEntry, 0, len(result.IPs))
	for _, ip := range sortedUniqueIPs(result.IPs) {
		entry := DomainIPSetEntry{
			Domain:        result.Domain,
			IP:            ip,
			Outcome:       result.Outcome,
			CachedAt:      result.CachedAt,
			TTL:           result.TTL,
			ExpiresAt:     result.ExpiresAt,
			Expired:       result.Expired,
			Stale:         result.Stale,
			ProxyEgressID: result.ProxyEgressID,
		}
		if result.TTL <= 0 {
			entry.Expired = true
			entry.Stale = true
		}
		set.entries[ip] = entry
		entries = append(entries, entry)
	}
	return entries
}

func (set *DomainIPSet) Store(domain string, ips []string, outcome CompiledOutcome, ttl time.Duration) []DomainIPSetEntry {
	now := set.clock()
	return set.StoreResult(CachedResult{
		Domain:        domain,
		IPs:           append([]string(nil), ips...),
		Outcome:       outcome,
		CachedAt:      now,
		TTL:           ttl,
		ExpiresAt:     now.Add(ttl),
		ProxyEgressID: outcome.ProxyEgressID,
	})
}

func (set *DomainIPSet) Match(ip string) (DomainIPSetEntry, bool) {
	entry, exists := set.entries[ip]
	if !exists {
		return DomainIPSetEntry{}, false
	}
	if set.isExpired(entry) {
		entry.Expired = true
		entry.Stale = true
		set.entries[ip] = entry
		return DomainIPSetEntry{}, false
	}
	return entry, true
}

func (set *DomainIPSet) Stale(ip string) (DomainIPSetEntry, bool) {
	entry, exists := set.entries[ip]
	if !exists || !set.isExpired(entry) {
		return DomainIPSetEntry{}, false
	}
	entry.Expired = true
	entry.Stale = true
	set.entries[ip] = entry
	return entry, true
}

func (set *DomainIPSet) Snapshot() DomainIPSetSnapshot {
	ips := make([]string, 0, len(set.entries))
	for key := range set.entries {
		ips = append(ips, key)
	}
	ips = sortedUniqueIPs(ips)

	entries := make([]DomainIPSetEntry, 0, len(ips))
	for _, ip := range ips {
		entries = append(entries, set.entries[ip])
	}
	return DomainIPSetSnapshot{Entries: entries}
}

func DomainIPSetFromSnapshot(snapshot DomainIPSetSnapshot, clock CacheClock) *DomainIPSet {
	set := NewDomainIPSet(clock)
	for _, entry := range snapshot.Entries {
		set.entries[entry.IP] = entry
	}
	return set
}

func (set *DomainIPSet) isExpired(entry DomainIPSetEntry) bool {
	return !set.clock().Before(entry.ExpiresAt)
}

func sortedUniqueIPs(ips []string) []string {
	seen := make(map[string]struct{})
	unique := make([]string, 0, len(ips))
	for _, ip := range ips {
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}
		canonical := addr.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		unique = append(unique, canonical)
	}
	sort.Slice(unique, func(left, right int) bool {
		leftAddr := netip.MustParseAddr(unique[left])
		rightAddr := netip.MustParseAddr(unique[right])
		return leftAddr.Less(rightAddr)
	})
	return unique
}
