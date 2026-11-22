variable "name" {
  type = string
}

variable "enabled" {
  type = bool
}

variable "subnet_id" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "instance_type" {
  type    = string
  default = "t4g.micro"
}

