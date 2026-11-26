# One customer-managed KMS key for the stack (secrets, EKS secrets, RDS,
# ElastiCache, log groups) and Secrets Manager entries for provider API keys.
#
# Terraform creates the secrets empty. The values are set outside Terraform
# (aws secretsmanager put-secret-value), so API keys never land in the state.

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}
data "aws_partition" "current" {}

locals {
  account_root = "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root"
}

data "aws_iam_policy_document" "key" {
  #checkov:skip=CKV_AWS_111:key policy: Resource "*" means this key only
  #checkov:skip=CKV_AWS_109:key policy: the account root statement is the AWS default that delegates to IAM
  #checkov:skip=CKV_AWS_356:key policy: Resource "*" means this key only
  # The account manages the key through IAM policies.
  statement {
    sid       = "AccountAdmin"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = [local.account_root]
    }
  }
  # CloudWatch Logs may encrypt this stack's log groups with the key.
  statement {
    sid       = "CloudWatchLogs"
    actions   = ["kms:Encrypt*", "kms:Decrypt*", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:Describe*"]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["logs.${data.aws_region.current.region}.amazonaws.com"]
    }
    condition {
      test     = "ArnLike"
      variable = "kms:EncryptionContext:aws:logs:arn"
      values   = ["arn:${data.aws_partition.current.partition}:logs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:*"]
    }
  }
}

resource "aws_kms_key" "this" {
  description             = "${var.name}: secrets, databases and logs"
  enable_key_rotation     = true
  rotation_period_in_days = 365
  deletion_window_in_days = var.key_deletion_window_days
  policy                  = data.aws_iam_policy_document.key.json
  tags                    = var.tags
}

resource "aws_kms_alias" "this" {
  name          = "alias/${var.name}"
  target_key_id = aws_kms_key.this.key_id
}

# Provider API keys. Rotation is done by the provider's console, not by AWS,
# so Secrets Manager rotation is not configured.
resource "aws_secretsmanager_secret" "provider" {
  #checkov:skip=CKV2_AWS_57:LLM provider keys are rotated in the provider's console; AWS cannot rotate them
  for_each                = toset(var.provider_secret_names)
  name                    = "${var.name}/${each.value}"
  description             = "LLM provider API key: ${each.value} (value set outside Terraform)"
  kms_key_id              = aws_kms_key.this.arn
  recovery_window_in_days = var.secret_recovery_window_days
  tags                    = var.tags
}
