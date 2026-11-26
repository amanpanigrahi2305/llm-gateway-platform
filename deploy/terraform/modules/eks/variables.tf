variable "name" {
  description = "Cluster name, also the name prefix."
  type        = string
}

variable "kubernetes_version" {
  description = "EKS Kubernetes version."
  type        = string
  default     = "1.35"
}

variable "private_subnet_ids" {
  description = "Private subnets for the control plane ENIs and the nodes."
  type        = list(string)
}

variable "api_public_cidrs" {
  description = "CIDRs allowed to reach the public API endpoint (e.g. your IP/32). Empty: private endpoint only."
  type        = list(string)
  default     = []
}

variable "kms_key_arn" {
  description = "KMS key for Kubernetes secrets, node disks and the log group."
  type        = string
}

variable "log_retention_days" {
  description = "Retention of control plane logs."
  type        = number
  default     = 365
}

variable "node_instance_types" {
  description = "Instance types for the node group (Graviton by default; the images are multi-arch)."
  type        = list(string)
  default     = ["t4g.medium", "t4g.large"]
}

variable "node_ami_type" {
  description = "EKS AMI type; must match the instance architecture."
  type        = string
  default     = "AL2023_ARM_64_STANDARD"
}

variable "node_capacity_type" {
  description = "SPOT (cheap, can be reclaimed) or ON_DEMAND."
  type        = string
  default     = "SPOT"
}

variable "node_min" {
  description = "Minimum nodes."
  type        = number
  default     = 2
}

variable "node_desired" {
  description = "Nodes at creation."
  type        = number
  default     = 2
}

variable "node_max" {
  description = "Maximum nodes."
  type        = number
  default     = 3
}

variable "node_disk_gb" {
  description = "Root disk per node (GiB)."
  type        = number
  default     = 30
}

variable "gateway_namespace" {
  description = "Namespace of the gateway's service account."
  type        = string
  default     = "llm-gateway"
}

variable "gateway_service_account" {
  description = "Service account name of the gateway pods."
  type        = string
  default     = "llm-gateway"
}

variable "gateway_secret_arns" {
  description = "Secrets the gateway may read (provider keys, Redis URL, database password)."
  type        = list(string)
}

variable "bedrock_model_arns" {
  description = "Bedrock foundation models / inference profiles the gateway may call."
  type        = list(string)
}

variable "tags" {
  description = "Tags for every resource."
  type        = map(string)
  default     = {}
}
