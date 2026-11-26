variable "name" {
  description = "Name prefix of the state bucket."
  type        = string
  default     = "llm-gw"
}

variable "region" {
  description = "AWS region of the state bucket."
  type        = string
  default     = "us-east-1"
}

variable "tags" {
  description = "Tags for every resource."
  type        = map(string)
  default     = { project = "llm-gateway-platform" }
}
