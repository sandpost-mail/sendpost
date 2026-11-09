// Stand-in for the application under test: it sends a verification mail
// through Sandpost's SMTP port. Every message carries the run id header, which
// is what keeps parallel CI runs apart. Replace this with your own code.
const nodemailer = require("nodemailer");

function transport() {
  return nodemailer.createTransport({
    host: process.env.SANDPOST_SMTP_HOST,
    port: Number(process.env.SANDPOST_SMTP_PORT || 1025),
    auth: { user: process.env.SANDPOST_SMTP_USER, pass: process.env.SANDPOST_SMTP_PASSWORD },
  });
}

async function sendVerificationMail(to, code) {
  await transport().sendMail({
    from: "app@example.test",
    to,
    subject: "Verify your account",
    text: `Your verification code is ${code}.\nOr open https://app.example.test/verify?code=${code}`,
    headers: { "X-Run-Id": process.env.SANDPOST_RUN_ID },
  });
}

module.exports = { sendVerificationMail };
