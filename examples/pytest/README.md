# pytest example

`test_signup_mail.py` sends a verification mail the way an application would (`app.py`, smtplib with an `X-Run-Id` header), then calls `sandpost extract` (`sandpost.py`). The `run_id` fixture in `conftest.py` gives every test its own id, `<SANDPOST_RUN_ID>-<random>`, so tests of one job never read each other's mail. See [../README.md](../README.md) for the variables and the exit codes.

```
pip install -r requirements.txt
export SANDPOST_URL=http://localhost:8080 SANDPOST_PROJECT=1 SANDPOST_TOKEN=spp_... \
       SANDPOST_SMTP_HOST=localhost SANDPOST_SMTP_USER=... SANDPOST_SMTP_PASSWORD=...
pytest -v     # needs the sandpost binary on PATH: git clone https://github.com/sandpost-mail/sendpost && go -C sendpost install ./cmd/sandpost
```

`ci.example.yml` is the GitHub Actions workflow. Not verified against a live server: only a Python syntax check was run on these files.
