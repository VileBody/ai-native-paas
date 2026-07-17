# Solo-development break-glass operator. This is intentionally broad and is
# not accepted as the controlled-beta human custody model. Replace it with
# named least-privilege operator groups before a beta window.
path "*" {
  capabilities = ["create", "read", "update", "patch", "delete", "list", "sudo"]
}
