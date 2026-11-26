# Run once per AWS account and region, before examples/aws-complete. Creates
# the S3 bucket for Terraform's remote state. Locking uses S3's own lockfile
# (backend setting use_lockfile = true, Terraform >= 1.10), so no DynamoDB
# table is needed. This folder keeps its own small state locally.

data "aws_caller_identity" "current" {}

locals {
  bucket = "${var.name}-tfstate-${data.aws_caller_identity.current.account_id}-${var.region}"
}

resource "aws_kms_key" "state" {
  #checkov:skip=CKV2_AWS_64:the default key policy (account root via IAM) is what this key needs
  description             = "${var.name}: Terraform state"
  enable_key_rotation     = true
  deletion_window_in_days = 7
  tags                    = var.tags
}

resource "aws_s3_bucket" "state" {
  #checkov:skip=CKV_AWS_18:access logging needs a second bucket; state access is in CloudTrail
  #checkov:skip=CKV_AWS_144:cross-region replication is not needed for a test-session state
  #checkov:skip=CKV2_AWS_62:no event notifications are needed for the state bucket
  bucket        = local.bucket
  force_destroy = false
  tags          = var.tags
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled" # every state version is kept, so a bad apply can be undone
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.state.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    id     = "old-state-versions"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = 90
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

data "aws_iam_policy_document" "state" {
  statement {
    sid       = "HTTPSOnly"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.state.arn, "${aws_s3_bucket.state.arn}/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "state" {
  bucket     = aws_s3_bucket.state.id
  policy     = data.aws_iam_policy_document.state.json
  depends_on = [aws_s3_bucket_public_access_block.state]
}
