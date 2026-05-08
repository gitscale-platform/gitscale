package billing

import (
	vault "github.com/hashicorp/vault/api"
)

var _ = (*vault.Client)(nil)
