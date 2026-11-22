variable "region" {
  type    = string
  default = "ap-south-1"
}

variable "project" {
  type    = string
  default = "dispatch"
}

variable "env" {
  type = string
}

variable "vpc_cidr" {
  type    = string
  default = "10.20.0.0/16"
}

variable "db_engine_version" {
  type    = string
  default = "17"
}

variable "deletion_protection" {
  type    = bool
  default = false
}

variable "log_retention_days" {
  type    = number
  default = 14
}

variable "log_level" {
  type    = string
  default = "info"
}

variable "max_receive_count" {
  type        = number
  default     = 8
  description = "Max delivery attempts before a message goes to the DLQ"
}

variable "worker_max_concurrency" {
  type    = number
  default = 5
}

variable "relay_schedule" {
  type    = string
  default = "rate(5 minutes)"
}

variable "relay_schedule_enabled" {
  type    = bool
  default = false
}

variable "alarm_email" {
  type    = string
  default = ""
}

variable "db_instance_class" {
  type    = string
  default = "db.t4g.micro"
}

variable "db_allocated_storage" {
  type    = number
  default = 20
}

variable "db_backup_retention_days" {
  type    = number
  default = 1
}

variable "db_multi_az" {
  type    = bool
  default = false
}

variable "nat_instance_type" {
  type    = string
  default = "t4g.nano"
}
