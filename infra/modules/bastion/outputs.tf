output "instance_id" {
  value = one(aws_instance.this[*].id)
}

output "security_group_ids" {
  value = aws_security_group.this[*].id
}
