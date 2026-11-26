variable "name" {
  description = "Name prefix (replication group ID, secret name)."
  type        = string
}

variable "vpc_id" {
  description = "VPC of the cluster."
  type        = string
}

variable "subnet_ids" {
  description = "Private subnets for the cache nodes."
  type        = list(string)
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to connect (the EKS cluster security group)."
  type        = list(string)
}

variable "kms_key_arn" {
  description = "KMS key for data at rest and the URL secret."
  type        = string
}

variable "engine_version" {
  description = "Redis OSS engine version."
  type        = string
  default     = "7.1"
}

variable "node_type" {
  description = "Cache node type."
  type        = string
  default     = "cache.t4g.micro"
}

variable "num_cache_clusters" {
  description = "1 for tests; 2 or more turns on automatic failover across AZs."
  type        = number
  default     = 1
}

variable "secret_recovery_window_days" {
  description = "Days a deleted secret can be restored (0 deletes at once)."
  type        = number
  default     = 7
}

variable "tags" {
  description = "Tags for every resource."
  type        = map(string)
  default     = {}
}
