# Unreleased

## Cloud accounts

- **Check an account**: `nullbot infra check NAME` (and **Check** in the
  desktop app) asks AWS (`sts get-caller-identity`) or Google Cloud
  (`compute project-info describe`) who an account's key signs in as, with
  the same clean environment plans use. It changes nothing and costs
  nothing. AWS keys record their account ID and ARN; a new key or project
  has to be checked again. A Google Cloud key that signs in but cannot use
  Compute Engine in its project yet gets a note saying what to fix.
- **Accounts are listed**: `nullbot infra accounts`, a line in `nullbot
  infra`, and `accounts` in the Cloud view's state; agents see, in
  `infra_status`, the accounts they may plan with.
- **Google Cloud plans use the account's project** when one is set, not
  only the key file's.
- [docs/INFRA.md](../INFRA.md) now covers adding and checking accounts, and
  the IAM permissions and Google Cloud roles plans need.
