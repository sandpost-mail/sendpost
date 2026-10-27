package policy

import (
	"errors"
	"fmt"
	"strings"
)

// Address limits (RFC 5321).
const (
	maxAddressLength = 254
	maxLocalLength   = 64
	maxDomainLength  = 253
	maxLabelLength   = 63
	// MaxAllowlistEntries bounds an allowlist.
	MaxAllowlistEntries = 500
)

// ErrAddress is returned for text that is not a plain, safe mailbox address.
var ErrAddress = errors.New("invalid address")

// Address is a strictly validated mailbox. The zero value is invalid. The
// validator is deliberately narrower than RFC 5322: no display names, no
// quoted local parts, no comments, no IP literals, no source routes and no
// non ASCII text. Anything it does not understand is rejected, because an
// address that is read differently by us and by the relay is how mail reaches
// the wrong person.
type Address struct {
	local  string
	domain string
}

// String returns local@domain with a lower case domain.
func (a Address) String() string { return a.local + "@" + a.domain }

// Local returns the local part as written.
func (a Address) Local() string { return a.local }

// Domain returns the lower case domain.
func (a Address) Domain() string { return a.domain }

// ParseAddress validates a bare addr-spec such as "dev@example.com".
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxAddressLength {
		return Address{}, fmt.Errorf("%w: empty or too long", ErrAddress)
	}
	at := strings.IndexByte(s, '@')
	if at < 0 || at != strings.LastIndexByte(s, '@') {
		return Address{}, fmt.Errorf("%w: must contain exactly one @", ErrAddress)
	}
	local, domain := s[:at], strings.ToLower(s[at+1:])
	if err := validLocal(local); err != nil {
		return Address{}, err
	}
	if err := validDomain(domain); err != nil {
		return Address{}, err
	}
	return Address{local: local, domain: domain}, nil
}

// localSpecials are the punctuation characters accepted in a local part. The
// characters that older relays treat as routing (% ! |) are not among them.
const localSpecials = "._-+=&'~#$*/?^{}"

func validLocal(l string) error {
	if l == "" || len(l) > maxLocalLength {
		return fmt.Errorf("%w: bad local part length", ErrAddress)
	}
	if l[0] == '.' || l[len(l)-1] == '.' || strings.Contains(l, "..") {
		return fmt.Errorf("%w: misplaced dot in local part", ErrAddress)
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte(localSpecials, c) >= 0:
		default:
			return fmt.Errorf("%w: character not allowed in local part", ErrAddress)
		}
	}
	return nil
}

func validDomain(d string) error {
	if d == "" || len(d) > maxDomainLength {
		return fmt.Errorf("%w: bad domain length", ErrAddress)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%w: domain needs at least two labels", ErrAddress)
	}
	for _, l := range labels {
		if l == "" || len(l) > maxLabelLength || l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("%w: bad domain label", ErrAddress)
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return fmt.Errorf("%w: character not allowed in domain", ErrAddress)
			}
		}
	}
	if allDigits(labels[len(labels)-1]) {
		return fmt.Errorf("%w: numeric top level domain", ErrAddress)
	}
	return nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// ErrAllowlist is returned for an allowlist entry that is not understood.
var ErrAllowlist = errors.New("invalid allowlist entry")

// entry is one validated allowlist rule: an exact address, or a whole domain
// when local is empty. There are no wildcards and no subdomain matching.
type entry struct {
	local  string // lower case, empty for a domain rule
	domain string
}

func (e entry) matches(a Address) bool {
	if e.domain != a.domain {
		return false
	}
	return e.local == "" || e.local == strings.ToLower(a.local)
}

// parseEntry accepts "local@domain", "@domain" and "domain".
func parseEntry(raw string) (entry, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return entry{}, fmt.Errorf("%w: empty", ErrAllowlist)
	}
	switch strings.Count(s, "@") {
	case 0:
		s = "@" + s
	case 1:
	default:
		return entry{}, fmt.Errorf("%w: more than one @", ErrAllowlist)
	}
	if strings.HasPrefix(s, "@") {
		if err := validDomain(s[1:]); err != nil {
			return entry{}, fmt.Errorf("%w: %q: %w", ErrAllowlist, raw, err)
		}
		return entry{domain: s[1:]}, nil
	}
	a, err := ParseAddress(s)
	if err != nil {
		return entry{}, fmt.Errorf("%w: %q: %w", ErrAllowlist, raw, err)
	}
	return entry{local: strings.ToLower(a.local), domain: a.domain}, nil
}

// NormalizeAllowlist validates entries and returns them in canonical lower
// case form ("user@example.com" or "@example.com"), without duplicates. Any
// invalid entry fails the whole list.
func NormalizeAllowlist(entries []string) ([]string, error) {
	if len(entries) > MaxAllowlistEntries {
		return nil, fmt.Errorf("%w: more than %d entries", ErrAllowlist, MaxAllowlistEntries)
	}
	seen := make(map[string]bool, len(entries))
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		e, err := parseEntry(raw)
		if err != nil {
			return nil, err
		}
		canon := e.local + "@" + e.domain
		if !seen[canon] {
			seen[canon] = true
			out = append(out, canon)
		}
	}
	return out, nil
}

func parseAllowlist(entries []string) ([]entry, error) {
	norm, err := NormalizeAllowlist(entries)
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(norm))
	for _, n := range norm {
		e, err := parseEntry(n)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func allowed(list []entry, a Address) bool {
	for _, e := range list {
		if e.matches(a) {
			return true
		}
	}
	return false
}
