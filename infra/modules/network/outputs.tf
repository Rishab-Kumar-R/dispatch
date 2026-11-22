output "vpc_id" {
  value = aws_vpc.this.id
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}

output "lambda_sg_id" {
  value = aws_security_group.lambda.id
}

output "nat_public_ip" {
  value = aws_eip.nat.public_ip
}
