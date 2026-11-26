# EKS for the gateway: private control plane endpoint (public access only from
# listed CIDRs), Kubernetes secrets encrypted with the stack's KMS key, all
# control plane logs, one managed node group (spot, IMDSv2 only) and EKS
# add-ons. Pods get AWS permissions through EKS Pod Identity, never node roles.

data "aws_partition" "current" {}

locals {
  partition = data.aws_partition.current.partition
  policy    = "arn:${local.partition}:iam::aws:policy"
}

# --- Cluster -----------------------------------------------------------------

data "aws_iam_policy_document" "cluster_assume" {
  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "cluster" {
  name               = "${var.name}-eks-cluster"
  assume_role_policy = data.aws_iam_policy_document.cluster_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "cluster" {
  role       = aws_iam_role.cluster.name
  policy_arn = "${local.policy}/AmazonEKSClusterPolicy"
}

# EKS uses the KMS key to encrypt Kubernetes Secrets (envelope encryption).
data "aws_iam_policy_document" "cluster_kms" {
  statement {
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:DescribeKey", "kms:CreateGrant"]
    resources = [var.kms_key_arn]
  }
}

resource "aws_iam_role_policy" "cluster_kms" {
  name   = "secrets-encryption"
  role   = aws_iam_role.cluster.id
  policy = data.aws_iam_policy_document.cluster_kms.json
}

resource "aws_cloudwatch_log_group" "cluster" {
  # EKS writes control plane logs here; the name is fixed by EKS.
  name              = "/aws/eks/${var.name}/cluster"
  retention_in_days = var.log_retention_days
  kms_key_id        = var.kms_key_arn
  tags              = var.tags
}

resource "aws_eks_cluster" "this" {
  name     = var.name
  version  = var.kubernetes_version
  role_arn = aws_iam_role.cluster.arn

  vpc_config {
    subnet_ids              = var.private_subnet_ids
    endpoint_private_access = true
    endpoint_public_access  = length(var.api_public_cidrs) > 0
    public_access_cidrs     = length(var.api_public_cidrs) > 0 ? var.api_public_cidrs : null
  }

  encryption_config {
    resources = ["secrets"]
    provider {
      key_arn = var.kms_key_arn
    }
  }

  enabled_cluster_log_types = ["api", "audit", "authenticator", "controllerManager", "scheduler"]

  access_config {
    authentication_mode                         = "API"
    bootstrap_cluster_creator_admin_permissions = true
  }

  tags       = var.tags
  depends_on = [aws_iam_role_policy_attachment.cluster, aws_cloudwatch_log_group.cluster]
}

# --- Nodes -------------------------------------------------------------------

data "aws_iam_policy_document" "node_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "node" {
  name               = "${var.name}-eks-node"
  assume_role_policy = data.aws_iam_policy_document.node_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "node" {
  for_each = toset([
    "AmazonEKSWorkerNodePolicy",
    "AmazonEC2ContainerRegistryReadOnly",
    "AmazonEKS_CNI_Policy",
  ])
  role       = aws_iam_role.node.name
  policy_arn = "${local.policy}/${each.value}"
}

# IMDSv2 only, and a hop limit of 1: pods cannot reach the node's instance
# credentials. Pods use Pod Identity instead.
resource "aws_launch_template" "node" {
  name_prefix = "${var.name}-node-"
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }
  block_device_mappings {
    device_name = "/dev/xvda"
    ebs {
      volume_size = var.node_disk_gb
      volume_type = "gp3"
      encrypted   = true
      kms_key_id  = var.kms_key_arn
    }
  }
  tag_specifications {
    resource_type = "instance"
    tags          = merge(var.tags, { Name = "${var.name}-node" })
  }
  tags = var.tags
}

resource "aws_eks_node_group" "default" {
  cluster_name    = aws_eks_cluster.this.name
  node_group_name = "default"
  node_role_arn   = aws_iam_role.node.arn
  subnet_ids      = var.private_subnet_ids
  capacity_type   = var.node_capacity_type
  instance_types  = var.node_instance_types
  ami_type        = var.node_ami_type

  scaling_config {
    min_size     = var.node_min
    desired_size = var.node_desired
    max_size     = var.node_max
  }
  update_config {
    max_unavailable = 1
  }
  launch_template {
    id      = aws_launch_template.node.id
    version = aws_launch_template.node.latest_version
  }

  tags       = var.tags
  depends_on = [aws_iam_role_policy_attachment.node]
}

# --- Add-ons -----------------------------------------------------------------

# No version given: EKS installs the default version for the cluster version.
resource "aws_eks_addon" "this" {
  for_each                    = toset(["vpc-cni", "kube-proxy", "coredns", "eks-pod-identity-agent", "metrics-server"])
  cluster_name                = aws_eks_cluster.this.name
  addon_name                  = each.value
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "OVERWRITE"
  tags                        = var.tags
  # coredns and metrics-server need nodes to become healthy.
  depends_on = [aws_eks_node_group.default]
}

# --- Pod Identity roles --------------------------------------------------------

data "aws_iam_policy_document" "pod_identity_assume" {
  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["pods.eks.amazonaws.com"]
    }
  }
}

# The gateway: read its own secrets and call Bedrock. Nothing else.
data "aws_iam_policy_document" "gateway" {
  statement {
    sid       = "ReadOwnSecrets"
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = var.gateway_secret_arns
  }
  statement {
    sid       = "DecryptOwnSecrets"
    actions   = ["kms:Decrypt"]
    resources = [var.kms_key_arn]
    condition {
      test     = "StringLike"
      variable = "kms:ViaService"
      values   = ["secretsmanager.*.amazonaws.com"]
    }
  }
  statement {
    sid       = "CallBedrock"
    actions   = ["bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"]
    resources = var.bedrock_model_arns
  }
}

resource "aws_iam_role" "gateway" {
  name               = "${var.name}-gateway"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy" "gateway" {
  name   = "gateway"
  role   = aws_iam_role.gateway.id
  policy = data.aws_iam_policy_document.gateway.json
}

resource "aws_eks_pod_identity_association" "gateway" {
  cluster_name    = aws_eks_cluster.this.name
  namespace       = var.gateway_namespace
  service_account = var.gateway_service_account
  role_arn        = aws_iam_role.gateway.arn
  tags            = var.tags
}

# AWS Load Balancer Controller: upstream policy for the pinned release.
resource "aws_iam_role" "lb_controller" {
  name               = "${var.name}-aws-load-balancer-controller"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy" "lb_controller" {
  name   = "aws-load-balancer-controller"
  role   = aws_iam_role.lb_controller.id
  policy = file("${path.module}/policies/aws-load-balancer-controller.json")
}

resource "aws_eks_pod_identity_association" "lb_controller" {
  cluster_name    = aws_eks_cluster.this.name
  namespace       = "kube-system"
  service_account = "aws-load-balancer-controller"
  role_arn        = aws_iam_role.lb_controller.arn
  tags            = var.tags
}
