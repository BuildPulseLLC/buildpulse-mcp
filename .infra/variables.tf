variable "environment" {
  type    = string
  default = "development"
}

variable "name" {
  type    = string
  default = "mcp-remote"
}

variable "description" {
  type    = string
  # AWS resource tags only permit: letters, spaces, digits, _ . / = + - : @
  # No parens, no em-dash. See reference_kms_tag_character_restrictions.
  default = "BuildPulse MCP server Streamable HTTP - managed by Terraform"
}

variable "version_tag" {
  type    = string
  default = "latest"
}

variable "backend_bucket" {
  type        = string
  description = "The name of the S3 bucket for Terraform state."
}

variable "domain" {
  type = object({
    development = string
    production  = string
  })
  default = {
    development = "mcp.dev.buildpulse.io"
    production  = "mcp.buildpulse.io"
  }
}

variable "priority" {
  type        = number
  description = "ALB listener-rule priority. Pick a value distinct from platform-api's (1001)."
  default     = 1002
}

variable "platform_api_url" {
  type        = string
  description = "Base URL of the platform-api this MCP talks to (no trailing slash)."
  default     = ""
}
