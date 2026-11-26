variable "name" {
  description = "Name prefix for every resource."
  type        = string
}

variable "cidr" {
  description = "VPC CIDR block. Split into a /20 per subnet."
  type        = string
  default     = "10.40.0.0/16"
}

variable "azs" {
  description = "Availability zones, at least 2 (EKS needs subnets in 2 AZs)."
  type        = list(string)
  validation {
    condition     = length(var.azs) >= 2
    error_message = "Give at least 2 availability zones."
  }
}

variable "interface_endpoints" {
  description = "Interface VPC endpoints (service names after com.amazonaws.<region>.)."
  type        = list(string)
  default     = ["ecr.api", "ecr.dkr", "sts", "secretsmanager", "logs", "bedrock-runtime"]
}

variable "kms_key_arn" {
  description = "KMS key that encrypts the flow log group."
  type        = string
}

variable "log_retention_days" {
  description = "How long CloudWatch keeps the flow logs."
  type        = number
  default     = 365
}

variable "tags" {
  description = "Tags for every resource."
  type        = map(string)
  default     = {}
}
