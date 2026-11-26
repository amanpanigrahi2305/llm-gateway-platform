# Phase 6a evidence: Terraform for AWS, checked offline only

Raw output of the offline checks on commit 806cdbb (2026-11-26 05:12 UTC).
**Nothing was applied and no AWS account was used**: no AWS credential
variables were set, `terraform init` ran without a backend, and every
`terraform test` uses mocked providers (`mock_provider`), which never call AWS
or Kubernetes. The example's main test is a *mocked* apply: Terraform invents
the computed values (IDs, ARNs) and creates nothing anywhere.

What is checked:

| Check | Result |
|---|---|
| `terraform fmt -check -recursive` | clean |
| `terraform validate` (6 modules, bootstrap, example) | 8 of 8 valid |
| `terraform test` with mocked providers | **13 runs passed, 0 failed** (11 in modules, 2 in the example) |
| tflint 0.66.0 with the AWS ruleset 0.50.0 | 0 issues |
| Checkov 3.4.6 on Terraform | 289 passed, **0 failed**, 13 skipped (each inline with its reason) |
| Checkov on the Dockerfile, chart and workflows | 48 / 87 / 180 passed, 0 failed |
| CI installer for terraform and tflint | pinned SHA-256 verified; a wrong checksum is rejected |
| `make aws-up` without `CONFIRM=yes` | refuses; Terraform is not started |

Found by the tests (fixed in 8475a51): the Redis and Postgres modules used
`for_each` over security group IDs. In the full stack that ID is the EKS
cluster's, which is unknown until apply, so the first real `terraform plan`
would have failed with "Invalid for_each argument". They use `count` now.

## Raw output

```
started: 2026-11-26 05:16 UTC  commit 806cdbb + working tree
terraform v1.17.2, tflint 0.66.0, checkov 3.4.6
AWS credential variables set in this shell: 0

## 1. CI installer: terraform and tflint with pinned SHA-256
installed terraform
installed tflint
wrong checksum: rejected

## 2. make tf-check (fmt, init without backend, validate, terraform test with mocked providers, tflint)
== terraform fmt -check
== modules/database
Success! The configuration is valid.
tests/database.tftest.hcl... in progress
  run "test_session_defaults"... pass
  run "production_settings"... pass
tests/database.tftest.hcl... tearing down
tests/database.tftest.hcl... pass
Success! 2 passed, 0 failed.
== modules/edge
Success! The configuration is valid.
tests/edge.tftest.hcl... in progress
  run "waf_without_domain"... pass
  run "domain_and_zone"... pass
tests/edge.tftest.hcl... tearing down
tests/edge.tftest.hcl... pass
Success! 2 passed, 0 failed.
== modules/eks
Success! The configuration is valid.
tests/eks.tftest.hcl... in progress
  run "private_api_and_locked_down_nodes"... pass
  run "public_api_only_from_listed_cidrs"... pass
tests/eks.tftest.hcl... tearing down
tests/eks.tftest.hcl... pass
Success! 2 passed, 0 failed.
== modules/network
Success! The configuration is valid.
tests/network.tftest.hcl... in progress
  run "two_azs_one_nat_six_endpoints"... pass
  run "rejects_a_single_az"... pass
tests/network.tftest.hcl... tearing down
tests/network.tftest.hcl... pass
Success! 2 passed, 0 failed.
== modules/redis
Success! The configuration is valid.
tests/redis.tftest.hcl... in progress
  run "encrypted_single_node"... pass
  run "two_nodes_fail_over"... pass
tests/redis.tftest.hcl... tearing down
tests/redis.tftest.hcl... pass
Success! 2 passed, 0 failed.
== modules/secrets
Success! The configuration is valid.
tests/secrets.tftest.hcl... in progress
  run "rotating_key_and_empty_secrets"... pass
tests/secrets.tftest.hcl... tearing down
tests/secrets.tftest.hcl... pass
Success! 1 passed, 0 failed.
== bootstrap
Success! The configuration is valid.
== examples/aws-complete
Success! The configuration is valid.
tests/plan.tftest.hcl... in progress
  run "mock_apply_without_domain"... pass
  run "plan_with_domain"... pass
tests/plan.tftest.hcl... tearing down
tests/plan.tftest.hcl... pass
Success! 2 passed, 0 failed.
== tflint
terraform checks passed
tf-check exit=0 in 136 s

## 3. checkov -d . (repo config: Dockerfile, chart, workflows, secrets, Terraform)
dockerfile scan results:
Passed checks: 48, Failed checks: 0, Skipped checks: 0
github_actions scan results:
Passed checks: 180, Failed checks: 0, Skipped checks: 0
helm scan results:
Passed checks: 87, Failed checks: 0, Skipped checks: 0
terraform scan results:
Passed checks: 289, Failed checks: 0, Skipped checks: 13
checkov exit=0

## 4. actionlint
actionlint: OK

## 5. make aws-up without CONFIRM=yes (must refuse; Terraform is not started)
This creates paid AWS resources (docs/COST.md). Run: make aws-up CONFIRM=yes
make: *** [Makefile:120: aws-up] Error 1
exit=2

## 6. Lines of Terraform
 1890 total
  443 total in tests
```
