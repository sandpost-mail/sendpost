# Playwright example

`signup.spec.js` fills the sign up form of a tiny demo application (`demo-app.js`, started by `playwright.config.js`), reads the verification code from the mail with `sandpost extract` (`sandpost.js`), and finishes the flow in the browser. The demo app mails through Sandpost's SMTP port and adds `X-Run-Id: $SANDPOST_RUN_ID`; replace it with your own application. See [../README.md](../README.md) for the variables and the exit codes.

```
npm install && npx playwright install chromium
export SANDPOST_URL=http://localhost:8080 SANDPOST_PROJECT=1 SANDPOST_TOKEN=spp_... \
       SANDPOST_SMTP_HOST=localhost SANDPOST_SMTP_USER=... SANDPOST_SMTP_PASSWORD=... \
       SANDPOST_RUN_ID=local-$(date +%s)
npx playwright test    # needs the sandpost binary on PATH: git clone https://github.com/sandpost-mail/sendpost && go -C sendpost install ./cmd/sandpost
```

With several workers, give each test its own run id (for example set `SANDPOST_RUN_ID` per worker in a fixture and pass it to the app) so two tests never read the same mail. `ci.example.yml` is the GitHub Actions workflow. Not verified against a live server: only `node --check` was run on these files.
