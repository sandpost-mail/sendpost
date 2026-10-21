package api

import (
	"html"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sandpost-mail/sandpost/internal/messages"
)

// The types below serialize to the JSON of Mailpit's API (see
// docs/mailpit/swagger.json): PascalCase keys without tags, ids as strings,
// every list present and never null.

// snippetRunes is how much of the body a summary carries, as in Mailpit.
const snippetRunes = 250

type mpAddress struct {
	Name    string
	Address string
}

type mpChecksums struct {
	MD5    string
	SHA1   string
	SHA256 string
}

type mpAttachment struct {
	PartID      string
	FileName    string
	ContentType string
	ContentID   string
	Size        uint64
	Checksums   mpChecksums
}

type mpListUnsubscribe struct {
	Header     string
	Links      []string
	Errors     string
	HeaderPost string
}

type mpSummary struct {
	ID          string
	MessageID   string
	Read        bool
	From        *mpAddress
	To          []mpAddress
	Cc          []mpAddress
	Bcc         []mpAddress
	ReplyTo     []mpAddress
	Subject     string
	Created     time.Time
	Username    string
	Tags        []string
	Size        uint64
	Attachments int
	Snippet     string
}

type mpMessage struct {
	ID              string
	MessageID       string
	From            *mpAddress
	To              []mpAddress
	Cc              []mpAddress
	Bcc             []mpAddress
	ReplyTo         []mpAddress
	ReturnPath      string
	Subject         string
	ListUnsubscribe mpListUnsubscribe
	Date            time.Time
	Tags            []string
	Username        string
	Text            string
	HTML            string
	Size            uint64
	Inline          []mpAttachment
	Attachments     []mpAttachment
}

// mpList is the envelope of the list and search endpoints.
type mpList struct {
	Total          int64       `json:"total"`
	Unread         int64       `json:"unread"`
	Count          int         `json:"count"`
	MessagesCount  int64       `json:"messages_count"`
	MessagesUnread int64       `json:"messages_unread"`
	Start          int         `json:"start"`
	Tags           []string    `json:"tags"`
	Messages       []mpSummary `json:"messages"`
}

func idString(id int64) string { return strconv.FormatInt(id, 10) }

func uintSize(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// parseAddress reads one address of the stored "Name <addr>" form. Text that
// is not an address becomes a name without address, like Mailpit does.
func parseAddress(s string) *mpAddress {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if a, err := mail.ParseAddress(s); err == nil {
		return &mpAddress{Name: a.Name, Address: a.Address}
	}
	return &mpAddress{Name: s}
}

// headerAddresses reads the addresses of an address header such as To. It
// returns ok false when the header is missing or does not parse.
func headerAddresses(h map[string][]string, key string) ([]mpAddress, bool) {
	vals := h[key]
	if len(vals) == 0 {
		return nil, false
	}
	list, err := mail.ParseAddressList(strings.Join(vals, ","))
	if err != nil {
		return nil, false
	}
	out := make([]mpAddress, 0, len(list))
	for _, a := range list {
		out = append(out, mpAddress{Name: a.Name, Address: a.Address})
	}
	return out, true
}

// recipientAddresses returns the stored recipient addresses of a kind.
func recipientAddresses(rs []messages.Recipient, kind string) []mpAddress {
	out := []mpAddress{}
	for _, r := range rs {
		if r.Kind == kind {
			out = append(out, mpAddress{Address: r.Address})
		}
	}
	return out
}

// addressesOf is the list of a header with display names, or the stored
// recipients of that kind when the header is missing or unparsable.
func addressesOf(h map[string][]string, rs []messages.Recipient, header, kind string) []mpAddress {
	if list, ok := headerAddresses(h, header); ok {
		return list
	}
	return recipientAddresses(rs, kind)
}

// bccOf lists the envelope recipients that are neither in To nor in Cc, plus
// the Bcc recipients stored from a header. This is how Mailpit derives Bcc.
func bccOf(rs []messages.Recipient, to, cc []mpAddress) []mpAddress {
	seen := map[string]bool{}
	for _, a := range append(append([]mpAddress{}, to...), cc...) {
		seen[strings.ToLower(a.Address)] = true
	}
	out := []mpAddress{}
	for _, r := range rs {
		key := strings.ToLower(r.Address)
		if (r.Kind == "bcc" || r.Kind == "envelope") && !seen[key] {
			seen[key] = true
			out = append(out, mpAddress{Address: r.Address})
		}
	}
	return out
}

var (
	reHiddenBlocks = regexp.MustCompile(`(?is)<(script|style|head)\b.*?</(script|style|head)>`)
	reTags         = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces       = regexp.MustCompile(`\s+`)
)

// snippet is the first 250 characters of the text, whitespace collapsed. A
// message without text uses its HTML with the markup removed.
func snippet(text, htmlHead string) string {
	src := text
	if strings.TrimSpace(src) == "" {
		src = html.UnescapeString(reTags.ReplaceAllString(reHiddenBlocks.ReplaceAllString(htmlHead, " "), " "))
	}
	src = strings.TrimSpace(reSpaces.ReplaceAllString(src, " "))
	if r := []rune(src); len(r) > snippetRunes {
		return string(r[:snippetRunes])
	}
	return src
}

func summaryOf(row messages.Row) mpSummary {
	to := addressesOf(row.Headers, row.Recipients, "To", "to")
	cc := addressesOf(row.Headers, row.Recipients, "Cc", "cc")
	replyTo, _ := headerAddresses(row.Headers, "Reply-To")
	if replyTo == nil {
		replyTo = []mpAddress{}
	}
	return mpSummary{
		ID: idString(row.ID), MessageID: strings.Trim(row.MessageID, "<>"), Read: row.ReadAt != nil,
		From: parseAddress(row.From), To: to, Cc: cc, Bcc: bccOf(row.Recipients, to, cc), ReplyTo: replyTo,
		Subject: row.Subject, Created: row.ReceivedAt, Tags: row.Tags, Size: uintSize(row.SizeBytes),
		Attachments: row.AttachmentCount, Snippet: snippet(row.TextHead, row.HTMLHead),
	}
}

func summariesOf(rows []messages.Row) []mpSummary {
	out := make([]mpSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, summaryOf(r))
	}
	return out
}

// attachmentOf maps one attachment. Checksums are empty when sums has no
// entry for it.
func attachmentOf(a messages.Attachment, sums map[int]messages.Checksums) mpAttachment {
	s := sums[a.Ordinal]
	return mpAttachment{
		PartID: strconv.Itoa(a.Ordinal), FileName: a.Filename, ContentType: a.ContentType, ContentID: a.ContentID,
		Size: uintSize(a.SizeBytes), Checksums: mpChecksums{MD5: s.MD5, SHA1: s.SHA1, SHA256: s.SHA256},
	}
}

// messageOf maps a message detail. Like Mailpit it lists inline parts that
// have a name or content id, and attachments that have one of them.
func messageOf(d messages.Detail, htmlBody string, sums map[int]messages.Checksums) mpMessage {
	h := d.Headers
	to := addressesOf(h, d.Recipients, "To", "to")
	cc := addressesOf(h, d.Recipients, "Cc", "cc")
	replyTo, _ := headerAddresses(h, "Reply-To")
	if replyTo == nil {
		replyTo = []mpAddress{}
	}
	from := parseAddress(d.From)
	m := mpMessage{
		ID: idString(d.ID), MessageID: strings.Trim(d.MessageID, "<>"), From: from, To: to, Cc: cc,
		Bcc: bccOf(d.Recipients, to, cc), ReplyTo: replyTo, ReturnPath: returnPath(h, from), Subject: d.Subject,
		ListUnsubscribe: listUnsubscribe(h), Date: messageDate(h, d.ReceivedAt), Tags: d.Tags,
		Text: d.Text, HTML: htmlBody, Size: uintSize(d.SizeBytes),
		Inline: []mpAttachment{}, Attachments: []mpAttachment{},
	}
	for _, a := range d.Attachments {
		if a.Filename == "" && a.ContentID == "" {
			continue
		}
		if a.Inline {
			m.Inline = append(m.Inline, attachmentOf(a, sums))
		} else {
			m.Attachments = append(m.Attachments, attachmentOf(a, sums))
		}
	}
	return m
}

func firstHeader(h map[string][]string, key string) string {
	if v := h[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func returnPath(h map[string][]string, from *mpAddress) string {
	if rp := strings.Trim(strings.TrimSpace(firstHeader(h, "Return-Path")), "<>"); rp != "" {
		return rp
	}
	if from != nil {
		return from.Address
	}
	return ""
}

// messageDate is the Date header, or the time of receipt without a usable one.
func messageDate(h map[string][]string, received time.Time) time.Time {
	if d, err := mail.ParseDate(firstHeader(h, "Date")); err == nil {
		return d
	}
	return received
}

var reAngle = regexp.MustCompile(`<([^<>]+)>`)

// listUnsubscribe summarizes the List-Unsubscribe headers: the value, and at
// most one mailto and one http(s) link.
func listUnsubscribe(h map[string][]string) mpListUnsubscribe {
	out := mpListUnsubscribe{Links: []string{}}
	hdr := firstHeader(h, "List-Unsubscribe")
	if hdr == "" {
		return out
	}
	out.Header = hdr
	out.HeaderPost = firstHeader(h, "List-Unsubscribe-Post")
	var mailto, web bool
	for _, m := range reAngle.FindAllStringSubmatch(hdr, -1) {
		link := strings.TrimSpace(m[1])
		switch lower := strings.ToLower(link); {
		case strings.HasPrefix(lower, "mailto:") && !mailto:
			mailto = true
			out.Links = append(out.Links, link)
		case (strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")) && !web:
			web = true
			out.Links = append(out.Links, link)
		}
	}
	return out
}
