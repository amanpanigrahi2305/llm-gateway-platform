variable "name" {
  description = "Name prefix (WAF ACL, log group, roles)."
  type        = string
}

variable "cluster_name" {
  description = "EKS cluster for the ExternalDNS Pod Identity association."
  type        = string
}

variable "domain_name" {
  description = "Gateway host name, e.g. llm.example.com. Empty: no certificate, no DNS (use the ALB name over HTTP)."
  type        = string
  default     = ""
}

variable "route53_zone_id" {
  description = "Route 53 hosted zone of domain_name. Empty: validate the certificate yourself, no ExternalDNS."
  type        = string
  default     = ""
}

variable "rate_limit_per_ip" {
  description = "WAF: requests per client IP per 5 minutes before it is blocked."
  type        = number
  default     = 2000
}

variable "kms_key_arn" {
  description = "KMS key for the WAF log group."
  type        = string
}

variable "log_retention_days" {
  description = "Retention of WAF logs."
  type        = number
  default     = 365
}

variable "tags" {
  description = "Tags for every resource."
  type        = map(string)
  default     = {}
}
