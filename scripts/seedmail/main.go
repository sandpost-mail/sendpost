// Command seedmail fills a running Sandpost with a realistic multi-project
// setup: users, groups, project roles and mail with HTML, inline images,
// links, attachments, Cc and Bcc. It goes through the public API and SMTP only.
//
//	go run ./scripts/seedmail
//
// Environment: SEED_API (default http://localhost:8080), SEED_SMTP (default
// localhost:1025), SEED_ADMIN_EMAIL and SEED_ADMIN_PASSWORD (default to
// ADMIN_EMAIL and ADMIN_PASSWORD, so source deploy/.env first), SEED_USER_PASSWORD.
// Users, groups, projects and roles are idempotent. Every run issues one new SMTP
// credential per project and sends the mail again.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/http/cookiejar"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"
)

type project struct{ slug, name string }

type user struct{ email, name string }

type group struct {
	name    string
	members []string          // user emails
	roles   map[string]string // project slug -> role
}

var (
	projects = []project{
		{"shop", "Acme Shop"},
		{"billing", "Billing"},
		{"hr", "HR Portal"},
		{"demo", "Demo"},
	}
	users = []user{
		{"alice@seed.test", "Alice Martin"},
		{"bob@seed.test", "Bob Keller"},
		{"carol@seed.test", "Carol Nguyen"},
		{"dave@seed.test", "Dave Rossi"},
	}
	// alice: owner of shop, viewer of billing. bob: member of shop, owner of
	// billing. carol: viewer of shop only. dave: member of hr only, so shop and
	// billing answer 404 for them.
	groups = []group{
		{"shop-owners", []string{"alice@seed.test"}, map[string]string{"shop": "owner"}},
		{"shop-devs", []string{"bob@seed.test"}, map[string]string{"shop": "member"}},
		{"shop-readonly", []string{"carol@seed.test"}, map[string]string{"shop": "viewer"}},
		{"billing-team", []string{"bob@seed.test"}, map[string]string{"billing": "owner"}},
		{"finance-viewers", []string{"alice@seed.test"}, map[string]string{"billing": "viewer"}},
		{"hr-team", []string{"dave@seed.test"}, map[string]string{"hr": "member"}},
	}
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run() error {
	api := newClient(env("SEED_API", "http://localhost:8080"))
	adminEmail := env("SEED_ADMIN_EMAIL", os.Getenv("ADMIN_EMAIL"))
	adminPass := env("SEED_ADMIN_PASSWORD", os.Getenv("ADMIN_PASSWORD"))
	if adminEmail == "" || adminPass == "" {
		return fmt.Errorf("set SEED_ADMIN_EMAIL and SEED_ADMIN_PASSWORD (or source deploy/.env)")
	}
	if err := api.login(adminEmail, adminPass); err != nil {
		return err
	}

	pids := map[string]int64{}
	for _, p := range projects {
		id, err := api.ensure("/projects", map[string]any{"name": p.name, "slug": p.slug}, func(m map[string]any) bool { return m["slug"] == p.slug })
		if err != nil {
			return fmt.Errorf("project %s: %w", p.slug, err)
		}
		pids[p.slug] = id
	}
	uids := map[string]int64{}
	pass := env("SEED_USER_PASSWORD", "Seed-Passw0rd-2026!")
	for _, u := range users {
		id, err := api.ensure("/users", map[string]any{"email": u.email, "name": u.name, "password": pass}, func(m map[string]any) bool { return m["email"] == u.email })
		if err != nil {
			return fmt.Errorf("user %s: %w", u.email, err)
		}
		uids[u.email] = id
	}
	for _, g := range groups {
		gid, err := api.ensure("/groups", map[string]any{"name": g.name}, func(m map[string]any) bool { return m["name"] == g.name })
		if err != nil {
			return fmt.Errorf("group %s: %w", g.name, err)
		}
		for _, email := range g.members {
			if err := api.do("PUT", fmt.Sprintf("/groups/%d/members/%d", gid, uids[email]), nil, nil); err != nil {
				return fmt.Errorf("member %s of %s: %w", email, g.name, err)
			}
		}
		for slug, role := range g.roles {
			if err := api.do("PUT", fmt.Sprintf("/projects/%d/groups/%d", pids[slug], gid), map[string]any{"role": role}, nil); err != nil {
				return fmt.Errorf("role %s on %s: %w", role, slug, err)
			}
		}
	}

	smtpAddr := env("SEED_SMTP", "localhost:1025")
	for _, p := range projects {
		var cred struct{ Username, Secret string }
		if err := api.do("POST", fmt.Sprintf("/projects/%d/credentials", pids[p.slug]), map[string]any{"label": "seed " + time.Now().Format("2006-01-02 15:04")}, &cred); err != nil {
			return fmt.Errorf("credential for %s: %w", p.slug, err)
		}
		msgs := mailsFor(p.slug)
		for _, m := range msgs {
			rcpt := append(append(append([]string{}, m.to...), m.cc...), m.bcc...)
			raw, err := m.build()
			if err != nil {
				return err
			}
			if err := smtp.SendMail(smtpAddr, smtp.PlainAuth("", cred.Username, cred.Secret, hostOf(smtpAddr)), envelopeFrom(m.from), rcpt, raw); err != nil {
				return fmt.Errorf("send %q to %s: %w", m.subject, p.slug, err)
			}
		}
		log.Printf("%-8s %d messages", p.slug, len(msgs))
	}
	log.Printf("done. users sign in with %q (password change is required at first login)", pass)
	return nil
}

// envelopeFrom strips the display name of "Name <addr>".
func envelopeFrom(from string) string {
	if a, err := netmail.ParseAddress(from); err == nil {
		return a.Address
	}
	return from
}

func hostOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i]
	}
	return addr
}

// client is a minimal session + CSRF API client.
type client struct {
	base string
	http *http.Client
	csrf string
}

func newClient(base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{base: strings.TrimRight(base, "/") + "/_/api/v1", http: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
}

func (c *client) login(email, password string) error {
	var out struct {
		CSRF               string `json:"csrf_token"`
		MustChangePassword bool   `json:"must_change_password"`
		User               struct {
			MustChangePassword bool `json:"must_change_password"`
		} `json:"user"`
	}
	if err := c.do("POST", "/auth/login", map[string]any{"email": email, "password": password}, &out); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if out.MustChangePassword || out.User.MustChangePassword {
		return fmt.Errorf("%s must change the password first: sign in once in the UI, then rerun with SEED_ADMIN_PASSWORD", email)
	}
	c.csrf = out.CSRF
	return nil
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, strings.TrimSpace(e.body))
}

func (c *client) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return &apiError{res.StatusCode, string(data)}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ensure creates the resource, or finds it in the listing when it exists.
func (c *client) ensure(path string, in map[string]any, match func(map[string]any) bool) (int64, error) {
	var created struct {
		ID int64 `json:"id"`
	}
	err := c.do("POST", path, in, &created)
	if err == nil {
		return created.ID, nil
	}
	var ae *apiError
	if !asAPIError(err, &ae) || ae.status != http.StatusConflict && ae.status != http.StatusBadRequest && ae.status != http.StatusUnprocessableEntity {
		return 0, err
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if lerr := c.do("GET", path, nil, &list); lerr != nil {
		return 0, err
	}
	for _, it := range list.Items {
		if match(it) {
			if id, ok := it["id"].(float64); ok {
				return int64(id), nil
			}
		}
	}
	return 0, err
}

func asAPIError(err error, target **apiError) bool {
	ae, ok := err.(*apiError)
	if ok {
		*target = ae
	}
	return ok
}

// ---- messages ----

type attachment struct {
	filename, ctype string
	data            []byte
}

type inlineImage struct {
	cid  string
	data []byte
}

type mail struct {
	from, subject  string
	to, cc, bcc    []string
	replyTo        string
	headers        [][2]string
	text, html     string
	images         []inlineImage
	attachments    []attachment
	calendar       string
	inReplyTo, ref string
	date           time.Time
}

func (m mail) build() ([]byte, error) {
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", m.from)
	h("To", strings.Join(m.to, ", "))
	if len(m.cc) > 0 {
		h("Cc", strings.Join(m.cc, ", "))
	}
	if m.replyTo != "" {
		h("Reply-To", m.replyTo)
	}
	h("Subject", mime.QEncoding.Encode("utf-8", m.subject))
	h("Date", m.date.Format(time.RFC1123Z))
	h("Message-ID", fmt.Sprintf("<%d.%s>", m.date.UnixNano(), strings.ReplaceAll(m.from, "@", ".")))
	if m.inReplyTo != "" {
		h("In-Reply-To", m.inReplyTo)
		h("References", m.ref)
	}
	h("MIME-Version", "1.0")
	for _, kv := range m.headers {
		h(kv[0], kv[1])
	}

	mixed := multipart.NewWriter(&b)
	h("Content-Type", fmt.Sprintf("multipart/mixed; boundary=%q", mixed.Boundary()))
	b.WriteString("\r\n")

	// alternative (text, related(html, images), calendar)
	altPart, err := nested(mixed, "multipart/alternative", func(alt *multipart.Writer) error {
		if err := qpPart(alt, "text/plain", m.text); err != nil {
			return err
		}
		if m.html != "" {
			if len(m.images) == 0 {
				if err := qpPart(alt, "text/html", m.html); err != nil {
					return err
				}
			} else {
				_, err := nested(alt, "multipart/related", func(rel *multipart.Writer) error {
					if err := qpPart(rel, "text/html", m.html); err != nil {
						return err
					}
					for _, im := range m.images {
						hd := textproto.MIMEHeader{}
						hd.Set("Content-Type", "image/png")
						hd.Set("Content-Transfer-Encoding", "base64")
						hd.Set("Content-ID", "<"+im.cid+">")
						hd.Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", im.cid+".png"))
						if err := b64Part(rel, hd, im.data); err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					return err
				}
			}
		}
		if m.calendar != "" {
			return qpPart(alt, `text/calendar; method=REQUEST; charset=utf-8`, m.calendar)
		}
		return nil
	})
	_ = altPart
	if err != nil {
		return nil, err
	}
	for _, a := range m.attachments {
		hd := textproto.MIMEHeader{}
		hd.Set("Content-Type", fmt.Sprintf("%s; name=%q", a.ctype, a.filename))
		hd.Set("Content-Transfer-Encoding", "base64")
		hd.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", a.filename))
		if err := b64Part(mixed, hd, a.data); err != nil {
			return nil, err
		}
	}
	if err := mixed.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func nested(parent *multipart.Writer, ctype string, fill func(*multipart.Writer) error) (io.Writer, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := fill(w); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	hd := textproto.MIMEHeader{}
	hd.Set("Content-Type", fmt.Sprintf("%s; boundary=%q", ctype, w.Boundary()))
	pw, err := parent.CreatePart(hd)
	if err != nil {
		return nil, err
	}
	_, err = pw.Write(buf.Bytes())
	return pw, err
}

func qpPart(w *multipart.Writer, ctype, body string) error {
	hd := textproto.MIMEHeader{}
	if !strings.Contains(ctype, "charset") {
		ctype += "; charset=utf-8"
	}
	hd.Set("Content-Type", ctype)
	hd.Set("Content-Transfer-Encoding", "quoted-printable")
	pw, err := w.CreatePart(hd)
	if err != nil {
		return err
	}
	qw := quotedprintable.NewWriter(pw)
	if _, err := qw.Write([]byte(body)); err != nil {
		return err
	}
	return qw.Close()
}

func b64Part(w *multipart.Writer, hd textproto.MIMEHeader, data []byte) error {
	pw, err := w.CreatePart(hd)
	if err != nil {
		return err
	}
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		if _, err := io.WriteString(pw, enc[:76]+"\r\n"); err != nil {
			return err
		}
		enc = enc[76:]
	}
	_, err = io.WriteString(pw, enc+"\r\n")
	return err
}

// ---- assets ----

// banner draws a horizontal gradient PNG, a stand-in for a logo or a product shot.
func banner(w, h int, from, to color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		t := float64(x) / float64(w-1)
		c := color.RGBA{
			R: uint8(float64(from.R)*(1-t) + float64(to.R)*t),
			G: uint8(float64(from.G)*(1-t) + float64(to.G)*t),
			B: uint8(float64(from.B)*(1-t) + float64(to.B)*t),
			A: 255,
		}
		for y := 0; y < h; y++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// pdf returns a one-page PDF with a title line.
func pdf(title string) []byte {
	content := fmt.Sprintf("BT /F1 22 Tf 72 720 Td (%s) Tj ET", title)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

// shell wraps content in a table based layout with inline styles, the way
// transactional mail is actually built.
func shell(brand, accent, body, footer string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>%[1]s</title>
<style>body{margin:0;background:#f4f4f7}a{color:%[2]s}@media(max-width:600px){.w{width:100%%!important}}</style></head>
<body style="margin:0;background:#f4f4f7;font-family:Helvetica,Arial,sans-serif;color:#333">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0"><tr><td align="center" style="padding:24px 8px">
<table role="presentation" class="w" width="600" cellpadding="0" cellspacing="0" style="background:#fff;border-radius:8px;overflow:hidden">
<tr><td><img src="cid:header" width="600" height="120" alt="%[1]s" style="display:block;width:100%%;height:auto"></td></tr>
<tr><td style="padding:28px 32px;font-size:15px;line-height:1.6">%[3]s</td></tr>
<tr><td style="padding:16px 32px;background:#fafafa;font-size:12px;color:#888">%[4]s</td></tr>
</table></td></tr></table></body></html>`, brand, accent, body, footer)
}

func button(href, label, color string) string {
	return fmt.Sprintf(`<a href="%s" style="display:inline-block;padding:12px 24px;background:%s;color:#fff;text-decoration:none;border-radius:6px;font-weight:bold">%s</a>`, href, color, label)
}

func mailsFor(slug string) []mail {
	base := time.Now().Add(-6 * time.Hour)
	at := func(i int) time.Time { return base.Add(time.Duration(i) * 47 * time.Minute) }
	hdr := func(a, b color.RGBA) []inlineImage { return []inlineImage{{"header", banner(600, 120, a, b)}} }

	switch slug {
	case "shop":
		blue, teal := color.RGBA{R: 37, G: 99, B: 235}, color.RGBA{R: 20, G: 184, B: 166}
		product := inlineImage{"product", banner(200, 200, color.RGBA{R: 251, G: 191, B: 36}, color.RGBA{R: 239, G: 68, B: 68})}
		order := mail{
			from: "orders@acme-shop.test", to: []string{"jane.doe@customer.test"}, cc: []string{"support@acme-shop.test", "alice@seed.test"},
			bcc: []string{"audit@acme-shop.test"}, replyTo: "support@acme-shop.test",
			subject: "Your order #10482 is confirmed", date: at(0),
			headers: [][2]string{{"X-Run-Id", "ci-4821"}, {"X-Mailer", "AcmeShop/3.2"}},
			text:    "Thanks for your order #10482.\nTotal: EUR 129.90\nTrack it: https://acme-shop.test/track/10482\n",
			html: shell("Acme Shop", "#2563eb", `<h1 style="margin:0 0 12px;font-size:22px">Thanks for your order, Jane</h1>
<p>We are preparing order <strong>#10482</strong>. You can <a href="https://acme-shop.test/track/10482?utm_source=email">track the parcel</a> at any time.</p>
<table role="presentation" width="100%" cellpadding="8" cellspacing="0" style="border-collapse:collapse;margin:16px 0">
<tr style="background:#f0f4ff"><th align="left">Item</th><th>Qty</th><th align="right">Price</th></tr>
<tr><td><img src="cid:product" width="48" height="48" alt="" style="vertical-align:middle;border-radius:4px"> Trail runner X2</td><td align="center">1</td><td align="right">EUR 99.90</td></tr>
<tr><td>Shipping</td><td align="center">1</td><td align="right">EUR 30.00</td></tr>
<tr style="border-top:2px solid #ddd"><td colspan="2"><strong>Total</strong></td><td align="right"><strong>EUR 129.90</strong></td></tr></table>
<p style="text-align:center">`+button("https://acme-shop.test/track/10482", "Track my order", "#2563eb")+`</p>
<p>Invoice attached. Questions? <a href="mailto:support@acme-shop.test">support@acme-shop.test</a></p>`,
				`Acme Shop, 12 rue des Lilas, 75011 Paris · <a href="https://acme-shop.test/legal">Legal</a> · <a href="https://acme-shop.test/unsubscribe?u=jane">Unsubscribe</a>`),
			images:      append(hdr(blue, teal), product),
			attachments: []attachment{{"invoice-10482.pdf", "application/pdf", pdf("Invoice 10482 - Acme Shop")}},
		}
		reset := mail{
			from: "no-reply@acme-shop.test", to: []string{"jane.doe@customer.test"},
			subject: "Reset your password", date: at(1),
			text: "Use this link within 30 minutes: https://acme-shop.test/reset?token=abc123\n",
			html: shell("Acme Shop", "#2563eb", `<h1 style="margin:0 0 12px;font-size:22px">Password reset</h1>
<p>Someone asked to reset the password for this address. If it was you, click below (valid 30 minutes).</p>
<p style="text-align:center">`+button("https://acme-shop.test/reset?token=abc123", "Choose a new password", "#2563eb")+`</p>
<p style="font-size:13px;color:#777">Button not working? Paste this link in your browser:<br><a href="https://acme-shop.test/reset?token=abc123">https://acme-shop.test/reset?token=abc123</a></p>`,
				"If you did not ask for this, ignore this message."),
			images: hdr(blue, teal),
		}
		news := mail{
			from: "Acme Shop <news@acme-shop.test>", to: []string{"subscribers@acme-shop.test"}, cc: []string{"marketing@acme-shop.test"},
			subject: "Soldes d'été : jusqu'à -40 % ☀️", date: at(2),
			headers: [][2]string{{"List-Unsubscribe", "<https://acme-shop.test/unsubscribe?u=all>, <mailto:unsubscribe@acme-shop.test>"}, {"X-Campaign", "summer-2026"}, {"Precedence", "bulk"}},
			text:    "Soldes d'été jusqu'à -40 %.\nhttps://acme-shop.test/sales\n",
			html: shell("Acme Shop", "#e11d48", `<h1 style="margin:0 0 12px;font-size:24px">Soldes d'été : jusqu'à -40&nbsp;%</h1>
<p>Découvrez notre sélection <em>été 2026</em> : chaussures, vestes légères et accessoires.</p>
<ul><li><a href="https://acme-shop.test/c/shoes?utm_campaign=summer">Chaussures</a></li><li><a href="https://acme-shop.test/c/jackets?utm_campaign=summer">Vestes</a></li><li><a href="https://acme-shop.test/c/bags?utm_campaign=summer">Sacs</a></li></ul>
<p><img src="https://cdn.acme-shop.test/img/summer-banner.jpg" width="536" alt="Bannière soldes" style="max-width:100%"></p>
<p style="text-align:center">`+button("https://acme-shop.test/sales", "Voir les offres", "#e11d48")+`</p>`,
				`<a href="https://acme-shop.test/unsubscribe?u=all">Se désabonner</a> · <a href="https://acme-shop.test/prefs">Préférences</a> <img src="https://track.acme-shop.test/open.gif?id=42" width="1" height="1" alt="">`),
			images: hdr(color.RGBA{R: 225, G: 29, B: 72}, color.RGBA{R: 251, G: 146, B: 60}),
		}
		thread := mail{
			from: "bob@seed.test", to: []string{"alice@seed.test"}, cc: []string{"carol@seed.test"},
			subject: "Re: Checkout 500 on staging", date: at(3),
			inReplyTo: "<thread-1@seed.test>", ref: "<thread-1@seed.test>",
			text: "Fixed in #482, the coupon lookup returned nil.\n\n> On Tue, Alice wrote:\n> checkout returns 500 when a coupon is applied\n",
			html: `<p>Fixed in <a href="https://github.com/acme/shop/pull/482">#482</a>, the coupon lookup returned <code>nil</code>.</p>
<blockquote style="border-left:3px solid #ccc;margin:0;padding-left:12px;color:#666">On Tue, Alice wrote:<br>checkout returns 500 when a coupon is applied</blockquote>`,
			attachments: []attachment{{"stacktrace.txt", "text/plain", []byte("panic: nil pointer dereference\ngoroutine 1 [running]:\nmain.applyCoupon()\n")}},
		}
		return []mail{order, reset, news, thread}
	case "billing":
		purple, pink := color.RGBA{R: 109, G: 40, B: 217}, color.RGBA{R: 219, G: 39, B: 119}
		inv := mail{
			from: "billing@ledger.test", to: []string{"cfo@client.test", "ap@client.test"}, cc: []string{"bob@seed.test", "finance@ledger.test"},
			subject: "Invoice INV-2026-0317 due 15 Nov", date: at(0),
			headers: [][2]string{{"X-Invoice", "INV-2026-0317"}},
			text:    "Invoice INV-2026-0317, EUR 4,800.00, due 15 Nov.\nPay: https://pay.ledger.test/INV-2026-0317\n",
			html: shell("Ledger", "#6d28d9", `<h1 style="margin:0 0 12px;font-size:22px">Invoice INV-2026-0317</h1>
<p>Amount due: <strong style="font-size:18px">EUR 4,800.00</strong> by <strong>15 November 2026</strong>.</p>
<p style="text-align:center">`+button("https://pay.ledger.test/INV-2026-0317", "Pay online", "#6d28d9")+`</p>
<p>The PDF and a CSV export of the line items are attached.</p>`, `Ledger SAS · <a href="https://ledger.test/terms">Terms</a>`),
			images:      hdr(purple, pink),
			attachments: []attachment{{"INV-2026-0317.pdf", "application/pdf", pdf("Invoice INV-2026-0317")}, {"lines.csv", "text/csv", []byte("item,qty,price\nPlatform,1,4000.00\nSupport,4,200.00\n")}},
		}
		fail := mail{
			from: "alerts@ledger.test", to: []string{"bob@seed.test"}, cc: []string{"alice@seed.test"}, bcc: []string{"oncall@ledger.test"},
			subject: "[Action required] Payment failed for subscription 8841", date: at(1),
			headers: [][2]string{{"X-Priority", "1"}, {"Importance", "high"}},
			text:    "Card ending 4242 was declined. Update it: https://ledger.test/billing/card\n",
			html: shell("Ledger", "#b91c1c", `<h1 style="margin:0 0 12px;font-size:22px;color:#b91c1c">Payment failed</h1>
<p>The card ending in <strong>4242</strong> was declined (insufficient funds). We retry in 3 days.</p>
<p>`+button("https://ledger.test/billing/card", "Update payment method", "#b91c1c")+`</p>`, "Automated message, do not reply."),
			images: hdr(color.RGBA{R: 185, G: 28, B: 28}, color.RGBA{R: 249, G: 115, B: 22}),
		}
		receipt := mail{
			from: "receipts@ledger.test", to: []string{"cfo@client.test"},
			subject: "Receipt for payment of EUR 1,200.00", date: at(2),
			text: "Received EUR 1,200.00, thank you.\n",
			html: `<h2>Receipt</h2><p>Received <b>EUR 1,200.00</b> on ` + at(2).Format("2 Jan 2006") + `. <a href="https://ledger.test/receipts/991">View receipt</a></p>`,
		}
		return []mail{inv, fail, receipt}
	case "hr":
		green, lime := color.RGBA{R: 22, G: 163, B: 74}, color.RGBA{R: 132, G: 204, B: 22}
		start := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
		ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//HR Portal//EN\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:onboarding-77@hr.test\r\n" +
			"DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z") + "\r\nDTSTART:" + start.UTC().Format("20060102T150405Z") + "\r\nDTEND:" + start.Add(time.Hour).UTC().Format("20060102T150405Z") +
			"\r\nSUMMARY:Onboarding kickoff\r\nORGANIZER:mailto:hr@hr.test\r\nATTENDEE:mailto:new.hire@hr.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		welcome := mail{
			from: "HR Portal <hr@hr.test>", to: []string{"new.hire@hr.test"}, cc: []string{"dave@seed.test", "manager@hr.test"},
			subject: "Bienvenue chez Acme, Léa ! Votre programme d'intégration", date: at(0),
			text: "Bienvenue Léa. Votre kickoff est dans 3 jours. https://hr.test/onboarding\n",
			html: shell("HR Portal", "#16a34a", `<h1 style="margin:0 0 12px;font-size:22px">Bienvenue, Léa&nbsp;!</h1>
<p>Nous sommes ravis de vous accueillir. Voici les premières étapes :</p>
<ol><li>Signer le <a href="https://hr.test/docs/contract">contrat</a></li><li>Configurer votre <a href="https://hr.test/it/laptop">poste</a></li><li>Rejoindre la réunion de lancement (invitation jointe)</li></ol>
<p style="text-align:center">`+button("https://hr.test/onboarding", "Ouvrir mon parcours", "#16a34a")+`</p>`, "Document confidentiel."),
			images:      hdr(green, lime),
			calendar:    ics,
			attachments: []attachment{{"livret-accueil.pdf", "application/pdf", pdf("Livret d'accueil")}},
		}
		leave := mail{
			from: "hr@hr.test", to: []string{"dave@seed.test"}, cc: []string{"manager@hr.test"},
			subject: "Leave request approved: 3 to 7 Nov", date: at(1),
			text: "Your leave is approved.\n",
			html: `<p>Your leave request <b>3 to 7 November</b> was approved by <a href="mailto:manager@hr.test">your manager</a>. <a href="https://hr.test/leave/55">Details</a></p>`,
		}
		return []mail{welcome, leave}
	default:
		return []mail{
			{from: "app@demo.test", to: []string{"dev@demo.test"}, subject: "Plain text smoke test", date: at(0), text: "Hello from the seed script.\n"},
			{from: "app@demo.test", to: []string{"dev@demo.test"}, cc: []string{"qa@demo.test"}, subject: "HTML smoke test", date: at(1),
				text: "Hello, see https://example.com\n", html: `<h3>Hello</h3><p>See <a href="https://example.com">example.com</a>.</p>`},
		}
	}
}
