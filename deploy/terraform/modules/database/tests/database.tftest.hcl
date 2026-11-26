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
  name                       = "t"
  vpc_id                     = "vpc-1"
  subnet_ids                 = ["subnet-1", "subnet-2"]
  allowed_security_group_ids = ["sg-cluster"]
  kms_key_arn                = "arn:aws:kms:us-east-1:111111111111:key/test"
}

run "test_session_defaults" {
  command = plan

  assert {
    condition     = aws_db_instance.this.manage_master_user_password && aws_db_instance.this.storage_encrypted
    error_message = "RDS must manage the password and encrypt storage"
  }
  assert {
    condition     = aws_db_instance.this.publicly_accessible == false && aws_db_instance.this.multi_az == false
    error_message = "private and single-AZ by default"
  }
  assert {
    condition     = aws_db_instance.this.deletion_protection == false && aws_db_instance.this.skip_final_snapshot
    error_message = "test sessions must be removable without a snapshot"
  }
  assert {
    condition     = anytrue([for p in aws_db_parameter_group.this.parameter : p.name == "rds.force_ssl" && p.value == "1"])
    error_message = "TLS must be required"
  }
  assert {
    condition     = aws_db_parameter_group.this.family == "postgres18"
    error_message = "parameter group family must follow the engine version"
  }
}

run "production_settings" {
  command = plan
  variables {
    deletion_protection = true
    skip_final_snapshot = false
    multi_az            = true
  }
  assert {
    condition     = aws_db_instance.this.deletion_protection && aws_db_instance.this.final_snapshot_identifier == "t-final" && aws_db_instance.this.multi_az
    error_message = "production settings must be honored"
  }
}
