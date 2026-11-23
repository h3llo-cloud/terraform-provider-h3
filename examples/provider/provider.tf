terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

# Credentials come from the environment:
#   export H3_KEY_ID="<api key id>"
#   export H3_SECRET_KEY="<api key secret>"
#   export H3_PROJECT_ID="<project uuid>"   # default project of all resources
# api_endpoint defaults to https://api.h3llo.cloud (or H3_API_ENDPOINT).
provider "h3" {}
