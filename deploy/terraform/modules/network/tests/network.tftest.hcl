# Offline: the AWS provider is mocked, so no credentials and no AWS calls.
mock_provider "aws" {
  # The AWS provider checks policy JSON, so mocked policy documents must be valid JSON.
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
  mock_data "aws_region" {
    defaults = { region = "us-east-1" }
  }
}

variables {
  name        = "t"
  azs         = ["us-east-1a", "us-east-1b"]
  kms_key_arn = "arn:aws:kms:us-east-1:111111111111:key/test"
}

run "two_azs_one_nat_six_endpoints" {
  command = plan

  assert {
    condition     = length(aws_subnet.public) == 2 && length(aws_subnet.private) == 2
    error_message = "want one public and one private subnet per AZ"
  }
  assert {
    condition     = alltrue([for s in aws_subnet.public : s.map_public_ip_on_launch == false])
    error_message = "public subnets must not give instances public IPs"
  }
  assert {
    condition     = aws_subnet.private[0].cidr_block == "10.40.128.0/20" && aws_subnet.public[1].cidr_block == "10.40.16.0/20"
    error_message = "unexpected subnet CIDRs"
  }
  assert {
    condition     = length(aws_vpc_endpoint.interface) == 6
    error_message = "want 6 interface endpoints (ECR api/dkr, STS, Secrets Manager, logs, Bedrock runtime)"
  }
  assert {
    condition     = aws_vpc_endpoint.interface["bedrock-runtime"].service_name == "com.amazonaws.us-east-1.bedrock-runtime"
    error_message = "Bedrock runtime endpoint missing"
  }
  assert {
    condition     = aws_vpc_endpoint.s3.vpc_endpoint_type == "Gateway"
    error_message = "S3 must be a (free) gateway endpoint"
  }
  assert {
    condition     = aws_flow_log.this.traffic_type == "ALL"
    error_message = "flow logs must capture all traffic"
  }
}

run "rejects_a_single_az" {
  command = plan
  variables {
    azs = ["us-east-1a"]
  }
  expect_failures = [var.azs]
}
