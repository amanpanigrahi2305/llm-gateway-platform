variable "name" {
  description = "Name prefix for every resource (also the EKS cluster name)."
  type        = string
  default     = "llm-gw"
}

variable "region" {
  description = "AWS region."
  type        = string
  default     = "us-east-1"
}

variable "azs" {
  description = "Two availability zones in the region."
  type        = list(string)
  default     = ["us-east-1a", "us-east-1b"]
}

variable "api_public_cidrs" {
  description = "CIDRs that may reach the EKS API (your IP/32). Empty: private endpoint only (then run kubectl/Helm from inside the VPC)."
  type        = list(string)
  default     = []
}

variable "domain_name" {
  description = "Gateway host name (e.g. llm.example.com). Empty: HTTP on the ALB's own name, no certificate."
  type        = string
  default     = ""
}

variable "route53_zone_id" {
  description = "Route 53 zone of domain_name: validates the certificate and lets ExternalDNS add the record."
  type        = string
  default     = ""
}

variable "provider_secret_names" {
  description = "Secrets Manager entries for provider API keys; each becomes an env var (openai-api-key -> OPENAI_API_KEY)."
  type        = list(string)
  default     = ["openai-api-key", "anthropic-api-key"]
}

variable "bedrock_model_ids" {
  description = "Bedrock foundation model IDs the gateway may call (IAM allows only these)."
  type        = list(string)
  default     = ["amazon.nova-micro-v1:0"]
}

variable "install_gateway" {
  description = "Install the gateway chart. Set the provider key values first (see the output set_provider_keys)."
  type        = bool
  default     = true
}

variable "gateway_image_repository" {
  description = "Gateway image (published by the release workflow)."
  type        = string
  default     = "ghcr.io/amanpanigrahi2305/llm-gateway"
}

variable "gateway_image_tag" {
  description = "Gateway image tag."
  type        = string
  default     = "v0.1.0"
}

variable "gateway_keys" {
  description = "Client keys, hashes only (make gen-key). Same fields as keys in config.example.yaml."
  type = list(object({
    id      = string
    hash    = string
    owner   = optional(string, "")
    team    = optional(string, "")
    aliases = optional(list(string), ["*"])
    policy  = optional(string, "default")
  }))
  validation {
    condition     = length(var.gateway_keys) > 0
    error_message = "Give at least one client key hash (make gen-key)."
  }
}

variable "tags" {
  description = "Tags for every resource."
  type        = map(string)
  default     = { project = "llm-gateway-platform" }
}
