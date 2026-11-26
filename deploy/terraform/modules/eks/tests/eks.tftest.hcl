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
  name                = "t"
  private_subnet_ids  = ["subnet-1", "subnet-2"]
  kms_key_arn         = "arn:aws:kms:us-east-1:111111111111:key/test"
  gateway_secret_arns = ["arn:aws:secretsmanager:us-east-1:111111111111:secret:t/openai-api-key"]
  bedrock_model_arns  = ["arn:aws:bedrock:us-east-1::foundation-model/amazon.nova-micro-v1:0"]
}

run "private_api_and_locked_down_nodes" {
  command = plan

  assert {
    condition     = aws_eks_cluster.this.vpc_config[0].endpoint_public_access == false && aws_eks_cluster.this.vpc_config[0].endpoint_private_access
    error_message = "without api_public_cidrs the API must be private only"
  }
  assert {
    condition     = length(aws_eks_cluster.this.enabled_cluster_log_types) == 5
    error_message = "all 5 control plane log types must be on"
  }
  assert {
    condition     = aws_eks_cluster.this.encryption_config[0].resources == toset(["secrets"])
    error_message = "Kubernetes secrets must be encrypted with the KMS key"
  }
  assert {
    condition     = aws_launch_template.node.metadata_options[0].http_tokens == "required" && aws_launch_template.node.metadata_options[0].http_put_response_hop_limit == 1
    error_message = "nodes must require IMDSv2 with hop limit 1 (pods cannot reach node credentials)"
  }
  assert {
    condition     = aws_eks_node_group.default.capacity_type == "SPOT"
    error_message = "the default node group is spot"
  }
  assert {
    condition     = aws_eks_pod_identity_association.gateway.namespace == "llm-gateway" && aws_eks_pod_identity_association.gateway.service_account == "llm-gateway"
    error_message = "gateway Pod Identity must bind llm-gateway/llm-gateway"
  }
  assert {
    condition     = alltrue([for s in data.aws_iam_policy_document.gateway.statement : !contains(s.actions, "*") && !contains(s.resources, "*")])
    error_message = "the gateway policy must not use * actions or * resources"
  }
  assert {
    condition     = contains(keys(aws_eks_addon.this), "eks-pod-identity-agent") && contains(keys(aws_eks_addon.this), "metrics-server")
    error_message = "Pod Identity agent and metrics-server add-ons are needed"
  }
}

run "public_api_only_from_listed_cidrs" {
  command = plan
  variables {
    api_public_cidrs = ["203.0.113.7/32"]
  }
  assert {
    condition     = aws_eks_cluster.this.vpc_config[0].endpoint_public_access && contains(aws_eks_cluster.this.vpc_config[0].public_access_cidrs, "203.0.113.7/32")
    error_message = "public access must be limited to the listed CIDRs"
  }
}
