# Security policy

## Supported versions

Sandpost is in early development. Security fixes land on `main` and in the latest release only.

## Reporting a vulnerability

Do not open a public issue. Report it privately through GitHub: **Security** tab of the repository, then **Report a vulnerability** ([direct link](https://github.com/sandpost-mail/sendpost/security/advisories/new)).

Include the affected version or commit, a description of the impact, and steps to reproduce. You should get a first answer within 7 days. Once a fix is released, the advisory is published with credit to the reporter unless you ask otherwise.

## Verifying releases

Release archives and container images carry signed build provenance:

```
gh attestation verify sandpost_<version>_linux_amd64.tar.gz --repo sandpost-mail/sendpost
gh attestation verify oci://ghcr.io/sandpost-mail/sendpost:<version> --repo sandpost-mail/sendpost
cosign verify ghcr.io/sandpost-mail/sendpost:<version> \
  --certificate-identity-regexp '^https://github.com/sandpost-mail/sendpost/\.github/workflows/release\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
