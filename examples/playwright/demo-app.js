// A tiny application under test: a sign up form that mails a verification
// code through Sandpost's SMTP port, and a page that checks the code. Every
// mail carries the run id header (X-Run-Id), which keeps parallel CI runs apart.
// Replace this with your own application, the test only needs a page and a mail.
const http = require("node:http");
const nodemailer = require("nodemailer");

const codes = new Map(); // email -> code
const port = Number(process.env.APP_PORT || 3000);

const page = (body) => `<!doctype html><meta charset="utf-8"><title>Demo</title>${body}`;

async function sendCode(to) {
  const code = String(Math.floor(100000 + Math.random() * 900000));
  codes.set(to, code);
  await nodemailer
    .createTransport({
      host: process.env.SANDPOST_SMTP_HOST,
      port: Number(process.env.SANDPOST_SMTP_PORT || 1025),
      auth: { user: process.env.SANDPOST_SMTP_USER, pass: process.env.SANDPOST_SMTP_PASSWORD },
    })
    .sendMail({
      from: "app@example.test",
      to,
      subject: "Your sign up code",
      text: `Your verification code is ${code}.`,
      headers: { "X-Run-Id": process.env.SANDPOST_RUN_ID },
    });
}

http
  .createServer(async (req, res) => {
    const url = new URL(req.url, `http://localhost:${port}`);
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    if (req.method === "POST" && url.pathname === "/signup") {
      const email = new URLSearchParams(await new Promise((r) => { let b = ""; req.on("data", (c) => (b += c)); req.on("end", () => r(b)); })).get("email");
      await sendCode(email);
      res.end(page(`<form method="post" action="/verify"><input type="hidden" name="email" value="${encodeURIComponent(email)}"><label>Code <input name="code"></label><button>Verify</button></form>`));
    } else if (req.method === "POST" && url.pathname === "/verify") {
      const f = new URLSearchParams(await new Promise((r) => { let b = ""; req.on("data", (c) => (b += c)); req.on("end", () => r(b)); }));
      const ok = codes.get(decodeURIComponent(f.get("email"))) === f.get("code");
      res.end(page(ok ? "<h1>Welcome</h1>" : "<h1>Wrong code</h1>"));
    } else {
      res.end(page(`<form method="post" action="/signup"><label>Email <input name="email" type="email"></label><button>Sign up</button></form>`));
    }
  })
  .listen(port, () => console.log(`demo app on :${port}`));
