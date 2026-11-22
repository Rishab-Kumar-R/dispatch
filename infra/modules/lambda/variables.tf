variable "name" {
  type = string
}

variable "zip_path" {
  type = string
}

variable "memory_size" {
  type    = number
  default = 256
}

variable "timeout" {
  type    = number
  default = 15
}

variable "log_retention_days" {
  type    = number
  default = 14
}

variable "environment" {
  type    = map(string)
  default = {}
}

variable "policy_statements" {
  type = list(object({
    actions   = list(string)
    resources = list(string)
  }))
  default = []
}

variable "vpc_subnet_ids" {
  type    = list(string)
  default = []
}

variable "vpc_security_group_ids" {
  type    = list(string)
  default = []
}
