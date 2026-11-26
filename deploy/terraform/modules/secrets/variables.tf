variable "name" {
  description = "Name prefix (KMS alias, secret names)."
  type        = string
}

variable "provider_secret_names" {
  description = "One Secrets Manager entry per provider API key, e.g. openai-api-key."
  type        = list(string)
  default     = ["openai-api-key", "anthropic-api-key"]
}

variable "key_deletion_window_days" {
  description = "Days before a deleted KMS key is really gone (7 to 30)."
  type        = number
  default     = 7
}

variable "secret_recovery_window_days" {
  description = "Days a deleted secret can be restored (0 deletes at once; 7 to 30 otherwise)."
  type        = number
  default     = 7
}

variable "tags" {
  description = "Tags for every resource."
  type        = map(string)
  default     = {}
}
