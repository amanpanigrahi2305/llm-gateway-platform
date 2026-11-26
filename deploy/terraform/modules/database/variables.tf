variable "name" {
  description = "Instance identifier and name prefix."
  type        = string
}

variable "vpc_id" {
  description = "VPC of the cluster."
  type        = string
}

variable "subnet_ids" {
  description = "Private subnets in at least 2 AZs (RDS requires it even for single-AZ)."
  type        = list(string)
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to connect (the EKS cluster security group)."
  type        = list(string)
}

variable "kms_key_arn" {
  description = "KMS key for storage, Performance Insights and the master secret."
  type        = string
}

variable "engine_version" {
  description = "Postgres major (or major.minor) version."
  type        = string
  default     = "18"
}

variable "instance_class" {
  description = "Instance class; the smallest is enough for the ledger."
  type        = string
  default     = "db.t4g.micro"
}

variable "allocated_storage_gb" {
  description = "Storage in GiB."
  type        = number
  default     = 20
}

variable "multi_az" {
  description = "Standby in a second AZ (production: true)."
  type        = bool
  default     = false
}

variable "backup_retention_days" {
  description = "Automated backup retention (production: 7 to 35)."
  type        = number
  default     = 1
}

variable "deletion_protection" {
  description = "Block deletion (production: true)."
  type        = bool
  default     = false
}

variable "skip_final_snapshot" {
  description = "Skip the snapshot on delete (production: false)."
  type        = bool
  default     = true
}

variable "tags" {
  description = "Tags for every resource."
  type        = map(string)
  default     = {}
}
