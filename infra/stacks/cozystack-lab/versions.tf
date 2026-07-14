terraform {
  required_version = ">= 1.10.0"

  backend "http" {}

  required_providers {
    twc = {
      source  = "tf.timeweb.cloud/timeweb-cloud/timeweb-cloud"
      version = "1.8.0"
    }

    talos = {
      source  = "siderolabs/talos"
      version = "0.11.0"
    }
  }

  encryption {
    key_provider "pbkdf2" "offline_recovery" {
      passphrase = var.state_passphrase
    }

    method "aes_gcm" "state" {
      keys = key_provider.pbkdf2.offline_recovery
    }

    state {
      method   = method.aes_gcm.state
      enforced = true
    }

    plan {
      method   = method.aes_gcm.state
      enforced = true
    }
  }
}

provider "twc" {}
provider "talos" {}
