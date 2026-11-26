# Edge of the gateway: AWS WAF for the ALB, an ACM certificate and a Pod
# Identity role for ExternalDNS (both optional: only with a domain).
#
# The ALB itself is created by the AWS Load Balancer Controller from the
# chart's Ingress; the Ingress annotations take this module's WAF ACL ARN and
# certificate ARN.
#
# Two rate limits, two jobs:
#   - WAF (here): per client IP, before traffic reaches the cluster. Stops
#     floods and scanners cheaply; knows nothing about API keys.
#   - Gateway (Redis): per API key, requests and tokens per minute and USD
#     budgets. Stops one tenant from using up shared capacity or money.

data "aws_partition" "current" {}

locals {
  with_domain = var.domain_name != ""
  with_zone   = var.domain_name != "" && var.route53_zone_id != ""
}

# --- WAF -----------------------------------------------------------------------

resource "aws_wafv2_web_acl" "this" {
  name        = var.name
  description = "${var.name} ALB: AWS managed rules + rate limit per IP"
  scope       = "REGIONAL"

  default_action {
    allow {}
  }

  rule {
    name     = "aws-common"
    priority = 1
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesCommonRuleSet"
        # Chat prompts can be long; the common set blocks bodies over 8 KB.
        # Count instead of block for that one rule, so long prompts pass.
        rule_action_override {
          name = "SizeRestrictions_BODY"
          action_to_use {
            count {}
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-aws-common"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "aws-known-bad-inputs"
    priority = 2
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesKnownBadInputsRuleSet" # includes Log4j (Log4Shell) patterns
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-known-bad-inputs"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "rate-per-ip"
    priority = 3
    action {
      block {}
    }
    statement {
      rate_based_statement {
        limit                 = var.rate_limit_per_ip
        evaluation_window_sec = 300
        aggregate_key_type    = "IP"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-rate-per-ip"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = var.name
    sampled_requests_enabled   = true
  }

  tags = var.tags
}

# WAF requires the log group name to start with aws-waf-logs-.
resource "aws_cloudwatch_log_group" "waf" {
  name              = "aws-waf-logs-${var.name}"
  retention_in_days = var.log_retention_days
  kms_key_id        = var.kms_key_arn
  tags              = var.tags
}

resource "aws_wafv2_web_acl_logging_configuration" "this" {
  resource_arn            = aws_wafv2_web_acl.this.arn
  log_destination_configs = [aws_cloudwatch_log_group.waf.arn]
  # Never log the Authorization header (it carries the client API key).
  redacted_fields {
    single_header {
      name = "authorization"
    }
  }
}

# --- Certificate (optional) ------------------------------------------------------

resource "aws_acm_certificate" "this" {
  count             = local.with_domain ? 1 : 0
  domain_name       = var.domain_name
  validation_method = "DNS"
  tags              = var.tags
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "validation" {
  for_each = local.with_zone ? {
    for o in aws_acm_certificate.this[0].domain_validation_options : o.domain_name => o
  } : {}
  zone_id = var.route53_zone_id
  name    = each.value.resource_record_name
  type    = each.value.resource_record_type
  records = [each.value.resource_record_value]
  ttl     = 300
}

resource "aws_acm_certificate_validation" "this" {
  count                   = local.with_zone ? 1 : 0
  certificate_arn         = aws_acm_certificate.this[0].arn
  validation_record_fqdns = [for r in aws_route53_record.validation : r.fqdn]
}

# --- ExternalDNS (optional): manages the gateway's record in the zone -------------

data "aws_iam_policy_document" "pod_identity_assume" {
  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["pods.eks.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "external_dns" {
  statement {
    sid       = "ChangeOwnZone"
    actions   = ["route53:ChangeResourceRecordSets", "route53:ListResourceRecordSets", "route53:ListTagsForResources"]
    resources = ["arn:${data.aws_partition.current.partition}:route53:::hostedzone/${var.route53_zone_id}"]
  }
  statement {
    sid       = "FindZones"
    actions   = ["route53:ListHostedZones"]
    resources = ["*"] # this API has no resource-level permissions
  }
}

resource "aws_iam_role" "external_dns" {
  count              = local.with_zone ? 1 : 0
  name               = "${var.name}-external-dns"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy" "external_dns" {
  count  = local.with_zone ? 1 : 0
  name   = "external-dns"
  role   = aws_iam_role.external_dns[0].id
  policy = data.aws_iam_policy_document.external_dns.json
}

resource "aws_eks_pod_identity_association" "external_dns" {
  count           = local.with_zone ? 1 : 0
  cluster_name    = var.cluster_name
  namespace       = "kube-system"
  service_account = "external-dns"
  role_arn        = aws_iam_role.external_dns[0].arn
  tags            = var.tags
}
