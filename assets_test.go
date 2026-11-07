package gitopsagent

import (
	"strings"
	"testing"
)

func TestAssetsEmbedded(t *testing.T) {
	for name, s := range map[string]string{
		"ConfigExampleTOML":   ConfigExampleTOML,
		"ServiceUnitTemplate": ServiceUnitTemplate,
		"UpdateServiceUnit":   UpdateServiceUnit,
		"UpdateTimerUnit":     UpdateTimerUnit,
	} {
		if s == "" {
			t.Errorf("%s is empty", name)
		}
	}
	if !strings.Contains(ServiceUnitTemplate, "\n[Service]") {
		t.Error("ServiceUnitTemplate has no [Service] section")
	}
	if !strings.Contains(ConfigExampleTOML, "\n[git]") {
		t.Error("ConfigExampleTOML has no [git] section")
	}
}
