variable "psql_url" {
  type    = string
  default = getenv("PSQL_URL")
}

variable "psql_prod_url" {
  type    = string
  default = getenv("PSQL_PROD_URL")
}

env "dev" {
  src = "file://schema.hcl"
  url = var.psql_url
  dev = var.psql_url
}

env "prod" {
  src = "file://schema.hcl"
  url = var.psql_prod_url
}
