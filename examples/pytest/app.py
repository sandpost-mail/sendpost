"""Stand-in for the application under test: sends a verification mail through
Sandpost's SMTP port. Every message carries the run id header, which keeps
parallel CI runs apart. Replace this with your own code."""
import os
import smtplib
from email.message import EmailMessage


def send_verification_mail(to, code, run_id):
    msg = EmailMessage()
    msg["From"] = "app@example.test"
    msg["To"] = to
    msg["Subject"] = "Verify your account"
    msg["X-Run-Id"] = run_id
    msg.set_content(f"Your verification code is {code}.\nOr open https://app.example.test/verify?code={code}\n")
    with smtplib.SMTP(os.environ["SANDPOST_SMTP_HOST"], int(os.environ.get("SANDPOST_SMTP_PORT", "1025"))) as smtp:
        smtp.login(os.environ["SANDPOST_SMTP_USER"], os.environ["SANDPOST_SMTP_PASSWORD"])
        smtp.send_message(msg)
