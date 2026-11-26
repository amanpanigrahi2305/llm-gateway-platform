# Offline: the AWS provider is mocked, so no credentials and no AWS calls.
mock_provider "aws" {
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
  # The AWS provider checks policy JSON, so mocked policy documents must be valid JSON.
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
}

variables {
  name         = "t"
  cluster_name = "t"
  kms_key_arn  = "arn:aws:kms:us-east-1:111111111111:key/test"
}

run "waf_without_domain" {
  command = plan
  assert {
    condition     = length(aws_wafv2_web_acl.this.rule) == 3
    error_message = "want AWS common rules, known bad inputs and the per-IP rate rule"
  }
  assert {
    condition     = anytrue([for r in aws_wafv2_web_acl.this.rule : r.name == "rate-per-ip" && r.statement[0].rate_based_statement[0].limit == 2000])
    error_message = "per-IP rate rule must use rate_limit_per_ip"
  }
  assert {
    condition     = aws_wafv2_web_acl_logging_configuration.this.redacted_fields[0].single_header[0].name == "authorization"
    error_message = "WAF logs must not contain the Authorization header"
  }
  assert {
    condition     = length(aws_acm_certificate.this) == 0 && length(aws_iam_role.external_dns) == 0
    error_message = "no certificate and no ExternalDNS without a domain"
  }
}

run "domain_and_zone" {
  command = plan
  variables {
    domain_name     = "llm.example.com"
    route53_zone_id = "Z0123456789"
  }
  override_resource {
    target          = aws_acm_certificate.this[0]
    override_during = plan
    values = {
      domain_validation_options = [{
        domain_name           = "llm.example.com"
        resource_record_name  = "_x.llm.example.com."
        resource_record_type  = "CNAME"
        resource_record_value = "_y.acm-validations.aws."
      }]
    }
  }
  assert {
    condition     = length(aws_acm_certificate.this) == 1 && length(aws_route53_record.validation) == 1
    error_message = "certificate with DNS validation in the zone"
  }
  assert {
    condition     = length(aws_eks_pod_identity_association.external_dns) == 1 && aws_eks_pod_identity_association.external_dns[0].service_account == "external-dns"
    error_message = "ExternalDNS gets its Pod Identity role"
  }
}
