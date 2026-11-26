# Offline: the providers are mocked, so no credentials and no AWS calls.
mock_provider "aws" {
  # The AWS provider checks policy JSON, so mocked policy documents must be valid JSON.
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
}
mock_provider "random" {}

variables {
  name                       = "t"
  vpc_id                     = "vpc-1"
  subnet_ids                 = ["subnet-1", "subnet-2"]
  allowed_security_group_ids = ["sg-cluster"]
  kms_key_arn                = "arn:aws:kms:us-east-1:111111111111:key/test"
}

run "encrypted_single_node" {
  command = plan
  assert {
    condition     = aws_elasticache_replication_group.this.transit_encryption_enabled && aws_elasticache_replication_group.this.at_rest_encryption_enabled
    error_message = "Redis must be encrypted in transit and at rest"
  }
  assert {
    condition     = aws_elasticache_replication_group.this.automatic_failover_enabled == false
    error_message = "one node cannot fail over"
  }
}

run "two_nodes_fail_over" {
  command = plan
  variables {
    num_cache_clusters = 2
  }
  assert {
    condition     = aws_elasticache_replication_group.this.automatic_failover_enabled && aws_elasticache_replication_group.this.multi_az_enabled
    error_message = "two nodes must turn on Multi-AZ failover"
  }
}
