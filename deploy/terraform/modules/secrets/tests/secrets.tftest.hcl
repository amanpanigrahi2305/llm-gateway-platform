# Offline: the AWS provider is mocked, so no credentials and no AWS calls.
mock_provider "aws" {
  # The AWS provider checks policy JSON, so mocked policy documents must be valid JSON.
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "111111111111" }
  }
  mock_data "aws_region" {
    defaults = { region = "us-east-1" }
  }
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
}

variables {
  name = "t"
}

run "rotating_key_and_empty_secrets" {
  command = plan
  assert {
    condition     = aws_kms_key.this.enable_key_rotation
    error_message = "the KMS key must rotate"
  }
  assert {
    condition     = toset(keys(aws_secretsmanager_secret.provider)) == toset(["openai-api-key", "anthropic-api-key"])
    error_message = "one secret per provider key"
  }
  assert {
    condition     = aws_secretsmanager_secret.provider["openai-api-key"].name == "t/openai-api-key"
    error_message = "secret names are prefixed with the stack name"
  }
}
