// Package gitopsagent embeds the installer assets that ship inside the
// gitops-agent binary: the example host config and the three systemd
// units. It lives at the module root, not under internal/, as a
// concession to go:embed -- embed patterns can't cross a ".." to reach a
// parent directory, and config.example.toml and systemd/ both live here.
// internal/installer is the only consumer.
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
