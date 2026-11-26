# Offline test of the whole example: every provider is mocked, so there are no
# AWS credentials, no AWS API calls and no Kubernetes cluster. The runs use
# command = apply because Terraform fills mocked computed values (ARNs, IDs)
# only in an apply; a mocked apply creates nothing anywhere.
#   terraform init -backend=false && terraform test
mock_provider "aws" {
  # The AWS provider checks policy JSON, so mocked policy documents must be valid JSON.
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
  mock_data "aws_region" {
    defaults = { region = "us-east-1" }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "111111111111" }
  }
  mock_resource "aws_eks_cluster" {
    defaults = {
      endpoint              = "https://example.gr7.us-east-1.eks.amazonaws.com"
      certificate_authority = [{ data = "Y2VydA==" }]
    }
  }
  mock_resource "aws_db_instance" {
    defaults = {
      address            = "llm-gw.example.us-east-1.rds.amazonaws.com"
      port               = 5432
      master_user_secret = [{ secret_arn = "arn:aws:secretsmanager:us-east-1:111111111111:secret:rds!db-1", kms_key_id = "k", secret_status = "active" }]
    }
  }
  # ARNs that one resource passes to another are checked by the provider, so
  # the mocks give them the right shape.
  mock_resource "aws_kms_key" {
    defaults = { arn = "arn:aws:kms:us-east-1:111111111111:key/00000000-0000-0000-0000-000000000000" }
  }
  mock_resource "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::111111111111:role/mock" }
  }
  mock_resource "aws_cloudwatch_log_group" {
    defaults = { arn = "arn:aws:logs:us-east-1:111111111111:log-group:mock" }
  }
  mock_resource "aws_wafv2_web_acl" {
    defaults = { arn = "arn:aws:wafv2:us-east-1:111111111111:regional/webacl/mock/00000000" }
  }
  mock_resource "aws_secretsmanager_secret" {
    defaults = { arn = "arn:aws:secretsmanager:us-east-1:111111111111:secret:mock" }
  }
  mock_resource "aws_launch_template" {
    defaults = { id = "lt-0123456789abcdef0", latest_version = 1 }
  }
}
mock_provider "helm" {}
mock_provider "random" {
  mock_resource "random_password" {
    defaults = { result = "Mock0000000000000000000000000000000000AA" }
  }
}

variables {
  gateway_keys = [{ id = "test", hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }]
}

run "mock_apply_without_domain" {
  command = apply

  assert {
    condition     = length(helm_release.external_dns) == 0
    error_message = "no ExternalDNS without a domain"
  }
  assert {
    condition = (
      yamldecode(helm_release.gateway[0].values[0]).ingress.annotations["alb.ingress.kubernetes.io/listen-ports"] == jsonencode([{ HTTP = 80 }])
      && !contains(keys(yamldecode(helm_release.gateway[0].values[0]).ingress.annotations), "alb.ingress.kubernetes.io/certificate-arn")
    )
    error_message = "without a domain the ALB listens on HTTP and has no certificate"
  }
  assert {
    condition     = yamldecode(helm_release.gateway[0].values[0]).ingress.annotations["alb.ingress.kubernetes.io/wafv2-acl-arn"] == "arn:aws:wafv2:us-east-1:111111111111:regional/webacl/mock/00000000"
    error_message = "the ALB must use the WAF ACL"
  }
  assert {
    condition = (
      contains([for o in yamldecode(helm_release.gateway[0].values[0]).secretsStore.objects : lookup(o, "jmesPath", "")], "password")
      && yamldecode(helm_release.gateway[0].values[0]).config.ledger.password_env == "GATEWAY_DB_PASSWORD"
    )
    error_message = "the RDS password must come from its managed secret through password_env"
  }
  assert {
    condition     = yamldecode(helm_release.gateway[0].values[0]).config.providers.bedrock == { type = "bedrock", region = "us-east-1" }
    error_message = "the gateway config must include the bedrock provider in the stack's region"
  }
  assert {
    condition     = length(output.set_provider_keys) == 2
    error_message = "one put-secret-value command per provider key"
  }
}

# A plan, not an apply: the certificate's validation records come from a
# for_each over values that the mock must provide at plan time.
run "plan_with_domain" {
  command = plan
  variables {
    domain_name     = "llm.example.com"
    route53_zone_id = "Z0123456789"
  }
  override_resource {
    target          = module.edge.aws_acm_certificate.this[0]
    override_during = plan
    values = {
      arn = "arn:aws:acm:us-east-1:111111111111:certificate/test"
      domain_validation_options = [{
        domain_name           = "llm.example.com"
        resource_record_name  = "_x.llm.example.com."
        resource_record_type  = "CNAME"
        resource_record_value = "_y.acm-validations.aws."
      }]
    }
  }
  assert {
    condition     = length(helm_release.external_dns) == 1 && module.edge.external_dns_enabled
    error_message = "ExternalDNS is installed with a domain and zone"
  }
}
