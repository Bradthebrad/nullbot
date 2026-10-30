# Unreleased

## Cloud accounts

- **Check an account**: `nullbot infra check NAME` (and **Check** in the
  desktop app) asks AWS (`sts get-caller-identity`) or Google Cloud
  (`compute project-info describe`) who an account's key signs in as, with
  the same clean environment plans use. It changes nothing and costs
  nothing. AWS keys record their account ID and ARN; a new key or project
  has to be checked again, and a key the cloud refuses stops showing as
  checked. A key that signs in but cannot be used yet (a Google Cloud
  project without Compute Engine or a role, an AWS region the account has
  not turned on) gets a note, kept with the account, saying what to fix. A
  wrong clock, a lost connection and a temporary AWS key without its session
  token are each told apart from a bad key.
- **Accounts are listed**: `nullbot infra accounts`, a line in `nullbot
  infra`, and `accounts` in the Cloud view's state; agents see, in
  `infra_status`, the accounts they may plan with.
- **Google Cloud plans use the account's project** when one is set, not
  only the key file's; replacing the key file with one from another project
  moves the account to it, unless its project was set apart.
- **Networks that inspect TLS**: plans and checks keep `AWS_CA_BUNDLE` and
  `CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE`.
- **Agents** see who a cloud account signs in as only once they may use it
  (`secrets_list`, and `nullbot infra` / `infra accounts` run by an agent),
  and an agent's command cannot allow a secret for agents (`nullbot secrets
  set --agent`).
- [docs/INFRA.md](../INFRA.md) now covers adding and checking accounts, and
  the IAM permissions and Google Cloud roles plans need.
