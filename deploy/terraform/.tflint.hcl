# tflint settings for every module and example (run from deploy/terraform:
# tflint --init && tflint --recursive --config "$PWD/.tflint.hcl").
plugin "terraform" {
  enabled = true
  preset  = "recommended"
}

plugin "aws" {
  enabled = true
  version = "0.50.0"
  source  = "github.com/terraform-linters/tflint-ruleset-aws"
}
