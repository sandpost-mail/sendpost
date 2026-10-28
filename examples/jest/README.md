# Jest example

`signup.test.js` sends a verification mail the way an application would (`app.js`, nodemailer over SMTP with an `X-Run-Id` header), then calls `sandpost extract` (`sandpost.js`) to read the code or the link. See [../README.md](../README.md) for the variables and the exit codes.

```
npm install
export SANDPOST_URL=http://localhost:8080 SANDPOST_PROJECT=1 SANDPOST_TOKEN=spp_... \
       SANDPOST_SMTP_HOST=localhost SANDPOST_SMTP_USER=... SANDPOST_SMTP_PASSWORD=... \
       SANDPOST_RUN_ID=local-$(date +%s)
npm test      # needs the sandpost binary on PATH: git clone https://github.com/sandpost-mail/sendpost && go -C sendpost install ./cmd/sandpost
```

`ci.example.yml` is the GitHub Actions workflow. Not verified against a live server: only `node --check` was run on these files.
