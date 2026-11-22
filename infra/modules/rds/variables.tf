variable "name" {
  type = string
}


variable "vpc_id" {
  type = string
}

variable "subnet_ids" {
  type = list(string)
}

variable "allowed_sg_ids" {
  type = list(string)
}

variable "engine_version" {
  type = string
}

variable "database_name" {
  type = string
}

variable "instance_class" {
  type    = string
  default = "db.t4g.micro"
}

variable "allocated_storage" {
  type    = number
  default = 20
}

variable "master_username" {
  type    = string
  default = "dbadmin"
}

variable "backup_retention_days" {
  type    = number
  default = 1
}

variable "multi_az" {
  type    = bool
  default = false
}

variable "deletion_protection" {
  type    = bool
  default = false
}
