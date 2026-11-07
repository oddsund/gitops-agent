// Package gitopsagent embeds the files `gitops-agent install` writes to a
// host.
//
// It lives at the module root only because go:embed cannot reach ".."
// and these files live at the root; internal/installer is the only
// importer. Embedding means the binary, not the synced repo or anything
// else on the host, decides what gets installed as root.
package gitopsagent

import _ "embed"

//go:embed config.example.toml
var ConfigExampleTOML string

//go:embed systemd/gitops-agent.service
var ServiceUnitTemplate string

//go:embed systemd/gitops-agent-update.service
var UpdateServiceUnit string

//go:embed systemd/gitops-agent-update.timer
var UpdateTimerUnit string
