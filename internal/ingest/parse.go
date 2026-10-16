package ingest

import (
	"bytes"
	"net/mail"
	"sort"
	"strings"

	"github.com/jhillyerd/enmime/v2"
)

// Limits that keep a hostile message from bloating rows or indexes. The raw
// message is always stored untouched, these only bound the derived fields.
const (
	maxParsedParts   = 1000
	maxHeaderValue   = 64 << 10
	maxSubjectRunes  = 2000
	maxAddressRunes  = 1000
	maxFilenameRunes = 500
	maxHeaderCount   = 2000
)

// Recipient kinds stored in message_recipients.
const (
	KindTo       = "to"
	KindCc       = "cc"
	KindBcc      = "bcc"
	KindEnvelope = "envelope"
)

// Recipient is one recipient address of a message.
type Recipient struct {
	Kind    string
	Address string
}

// Attachment describes an attachment or inline part. Ordinal is the position
// in the parsed attachments followed by the parsed inlines.
type Attachment struct {
	Ordinal     int
	Filename    string
	ContentType string
	Size        int64
	ContentID   string
	Inline      bool
}

// Parsed is the structured view of a raw message, safe to store in Postgres.
type Parsed struct {
	MessageID   string
	From        string
	Subject     string
	Text        string
	HTML        string
	Headers     map[string][]string
	Recipients  []Recipient
	Attachments []Attachment
	Links       []string
}

var parser = enmime.NewParser(
	enmime.MaxMIMEParts(maxParsedParts),
	enmime.SetReadPartErrorPolicy(enmime.AllowCorruptTextPartErrorPolicy),
	enmime.SkipMalformedParts(true),
)

// parseEnvelope runs enmime and returns the envelope, or nil when the message
// cannot be parsed at all.
func parseEnvelope(raw []byte) *enmime.Envelope {
	root, err := parser.ReadParts(bytes.NewReader(raw))
	if err != nil || root == nil {
		return nil
	}
	env, err := parser.EnvelopeFromPart(root)
	if err != nil {
		return nil
	}
	return env
}

// Parse extracts the structured fields of raw. It never fails: an unparsable
// message yields a Parsed with only the envelope recipients, so capture
// always succeeds. envelopeFrom and envelopeTo come from the SMTP transaction.
func Parse(raw []byte, envelopeFrom string, envelopeTo []string) Parsed {
	p := Parsed{Headers: map[string][]string{}}
	env := parseEnvelope(raw)
	if env != nil {
		fillFromEnvelope(&p, env)
	}
	if p.From == "" {
		p.From = truncateRunes(clean(strings.TrimSpace(envelopeFrom)), maxAddressRunes)
	}
	p.Recipients = dedupeRecipients(append(p.Recipients, envelopeRecipients(envelopeTo)...))
	p.Links = ExtractLinks(p.HTML, p.Text)
	return p
}

func fillFromEnvelope(p *Parsed, env *enmime.Envelope) {
	p.MessageID = truncateRunes(clean(strings.TrimSpace(env.GetHeader("Message-Id"))), maxAddressRunes)
	p.Subject = truncateRunes(clean(strings.TrimSpace(env.GetHeader("Subject"))), maxSubjectRunes)
	p.Text = clean(env.Text)
	p.HTML = clean(env.HTML)
	p.From = formatFrom(env)
	p.Headers = collectHeaders(env)
	p.Recipients = headerRecipients(env)
	p.Attachments = collectAttachments(env)
}

func formatFrom(env *enmime.Envelope) string {
	list, err := env.AddressList("From")
	if err == nil && len(list) > 0 {
		a := list[0]
		s := a.Address
		if a.Name != "" {
			s = a.Name + " <" + a.Address + ">"
		}
		return truncateRunes(clean(s), maxAddressRunes)
	}
	return truncateRunes(clean(strings.TrimSpace(env.GetHeader("From"))), maxAddressRunes)
}

func collectHeaders(env *enmime.Envelope) map[string][]string {
	keys := env.GetHeaderKeys()
	sort.Strings(keys)
	if len(keys) > maxHeaderCount {
		keys = keys[:maxHeaderCount]
	}
	out := make(map[string][]string, len(keys))
	for _, k := range keys {
		vals := env.GetHeaderValues(k)
		cleaned := make([]string, 0, len(vals))
		for _, v := range vals {
			cleaned = append(cleaned, truncateBytes(clean(v), maxHeaderValue))
		}
		out[clean(k)] = cleaned
	}
	return out
}

func headerRecipients(env *enmime.Envelope) []Recipient {
	var out []Recipient
	for _, h := range []struct{ header, kind string }{
		{"To", KindTo}, {"Cc", KindCc}, {"Bcc", KindBcc},
	} {
		list, err := env.AddressList(h.header)
		if err != nil {
			continue
		}
		out = append(out, addressesToRecipients(list, h.kind)...)
	}
	return out
}

func addressesToRecipients(list []*mail.Address, kind string) []Recipient {
	out := make([]Recipient, 0, len(list))
	for _, a := range list {
		if addr := truncateRunes(clean(strings.TrimSpace(a.Address)), maxAddressRunes); addr != "" {
			out = append(out, Recipient{Kind: kind, Address: addr})
		}
	}
	return out
}

func envelopeRecipients(to []string) []Recipient {
	out := make([]Recipient, 0, len(to))
	for _, a := range to {
		if addr := truncateRunes(clean(strings.TrimSpace(a)), maxAddressRunes); addr != "" {
			out = append(out, Recipient{Kind: KindEnvelope, Address: addr})
		}
	}
	return out
}

func dedupeRecipients(in []Recipient) []Recipient {
	seen := make(map[Recipient]struct{}, len(in))
	out := make([]Recipient, 0, len(in))
	for _, r := range in {
		key := Recipient{Kind: r.Kind, Address: strings.ToLower(r.Address)}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r)
	}
	return out
}

// attachmentParts returns the parts that count as attachments, in the stable
// order used for Attachment.Ordinal.
func attachmentParts(env *enmime.Envelope) []*enmime.Part {
	parts := make([]*enmime.Part, 0, len(env.Attachments)+len(env.Inlines))
	parts = append(parts, env.Attachments...)
	return append(parts, env.Inlines...)
}

func collectAttachments(env *enmime.Envelope) []Attachment {
	parts := attachmentParts(env)
	out := make([]Attachment, 0, len(parts))
	for i, part := range parts {
		out = append(out, Attachment{
			Ordinal:     i,
			Filename:    truncateRunes(clean(part.FileName), maxFilenameRunes),
			ContentType: truncateRunes(clean(part.ContentType), maxAddressRunes),
			Size:        int64(len(part.Content)),
			ContentID:   strings.Trim(clean(part.ContentID), "<> "),
			Inline:      i >= len(env.Attachments),
		})
	}
	return out
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// AttachmentContent re-parses raw and returns the attachment with the given
// ordinal and its decoded bytes. ok is false when there is no such attachment.
func AttachmentContent(raw []byte, ordinal int) (a Attachment, content []byte, ok bool) {
	env := parseEnvelope(raw)
	if env == nil {
		return Attachment{}, nil, false
	}
	parts := attachmentParts(env)
	if ordinal < 0 || ordinal >= len(parts) {
		return Attachment{}, nil, false
	}
	metas := collectAttachments(env)
	return metas[ordinal], parts[ordinal].Content, true
}
