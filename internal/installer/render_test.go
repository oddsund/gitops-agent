package installer

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	gitopsagent "github.com/oddsund/gitops-agent"
	"github.com/oddsund/gitops-agent/internal/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	goldenUser    = "pi"
	goldenRepoURL = "git@github.com:example/homelab.git"
	goldenSSHKey  = "/home/pi/.ssh/id_ed25519"
	goldenClone   = "/opt/gitops-agent/repo"
)

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s (rerun with -update to regenerate)\ngot:\n%s", path, got)
	}
}

func TestRenderServiceUnit_Golden(t *testing.T) {
	got, err := renderServiceUnit(gitopsagent.ServiceUnitTemplate, goldenUser, goldenClone)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "gitops-agent.service.golden", got)
}

func TestRenderConfigTOML_Golden(t *testing.T) {
	got, err := renderConfigTOML(gitopsagent.ConfigExampleTOML, goldenRepoURL, goldenSSHKey, goldenClone)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "config.toml.golden", got)
}

// TestRender_MatchesProvisionSed runs the sed expressions from homelab's
// iac/provision.bash so the Go rendering stays byte-for-byte equivalent.
func TestRender_MatchesProvisionSed(t *testing.T) {
	if _, err := exec.LookPath("sed"); err != nil {
		t.Skip("sed not available")
	}
	runSed := func(input string, exprs ...string) string {
		t.Helper()
		var args []string
		for _, e := range exprs {
			args = append(args, "-e", e)
		}
		cmd := exec.Command("sed", args...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	wantSvc := runSed(gitopsagent.ServiceUnitTemplate,
		"s/^User=.*/User="+goldenUser+"/",
		"s#^ReadWritePaths=.*#ReadWritePaths="+goldenClone+"#")
	gotSvc, err := renderServiceUnit(gitopsagent.ServiceUnitTemplate, goldenUser, goldenClone)
	if err != nil {
		t.Fatal(err)
	}
	if gotSvc != wantSvc {
		t.Errorf("service unit differs from sed output\ngot:\n%s\nwant:\n%s", gotSvc, wantSvc)
	}

	wantCfg := runSed(gitopsagent.ConfigExampleTOML,
		`s#^repo_url = .*#repo_url = "`+goldenRepoURL+`"#`,
		`s#^ssh_key_path = .*#ssh_key_path = "`+goldenSSHKey+`"#`,
		`s#^clone_path = .*#clone_path = "`+goldenClone+`"#`)
	gotCfg, err := renderConfigTOML(gitopsagent.ConfigExampleTOML, goldenRepoURL, goldenSSHKey, goldenClone)
	if err != nil {
		t.Fatal(err)
	}
	if gotCfg != wantCfg {
		t.Errorf("config differs from sed output\ngot:\n%s\nwant:\n%s", gotCfg, wantCfg)
	}
}

func TestSubstituteLines(t *testing.T) {
	subs := []lineSub{{"User=", "pi"}}

	t.Run("zero matches", func(t *testing.T) {
		_, err := substituteLines("a\nb\n", subs)
		if err == nil || !strings.Contains(err.Error(), "0 lines") {
			t.Errorf("err = %v, want it to mention 0 lines", err)
		}
	})
	t.Run("two matches", func(t *testing.T) {
		_, err := substituteLines("User=a\nUser=b\n", subs)
		if err == nil || !strings.Contains(err.Error(), "2 lines") {
			t.Errorf("err = %v, want it to mention 2 lines", err)
		}
	})
	t.Run("comment and indented lines do not match", func(t *testing.T) {
		got, err := substituteLines("# User=x\n User=y\nUser=z\n", subs)
		if err != nil {
			t.Fatal(err)
		}
		if want := "# User=x\n User=y\nUser=pi\n"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("final newline state is kept", func(t *testing.T) {
		got, _ := substituteLines("a\nUser=z", subs)
		if got != "a\nUser=pi" {
			t.Errorf("got %q", got)
		}
		got, _ = substituteLines("a\nUser=z\n", subs)
		if got != "a\nUser=pi\n" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("other lines are byte identical", func(t *testing.T) {
		in := "  x \t\r\n\n# c\nUser=z\n\n"
		got, _ := substituteLines(in, subs)
		if want := strings.Replace(in, "User=z", "User=pi", 1); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestRenderPrefixesAreDisjoint(t *testing.T) {
	prefixes := []string{"User=", "ReadWritePaths=", "repo_url = ", "ssh_key_path = ", "clone_path = "}
	for i, a := range prefixes {
		for j, b := range prefixes {
			if i != j && strings.HasPrefix(a, b) {
				t.Errorf("prefix %q starts with %q", a, b)
			}
		}
	}
}

func TestRender_RejectsLineBreaks(t *testing.T) {
	if _, err := renderServiceUnit(gitopsagent.ServiceUnitTemplate, "pi\nExecStartPre=/bin/x", goldenClone); err == nil {
		t.Error("user with newline accepted")
	}
	if _, err := renderServiceUnit(gitopsagent.ServiceUnitTemplate, "pi", "/a\rb"); err == nil {
		t.Error("path with carriage return accepted")
	}
}

func TestTomlQuote(t *testing.T) {
	in := `a"b\c`
	got := tomlQuote(in)
	if got != `"a\"b\\c"` {
		t.Errorf("tomlQuote = %s", got)
	}
	var doc struct{ V string }
	if _, err := toml.Decode("V = "+got, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.V != in {
		t.Errorf("round trip = %q, want %q", doc.V, in)
	}
}

func TestRenderedConfigLoads(t *testing.T) {
	got, err := renderConfigTOML(gitopsagent.ConfigExampleTOML, goldenRepoURL, goldenSSHKey, goldenClone)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Git.ClonePath != goldenClone {
		t.Errorf("ClonePath = %q", cfg.Git.ClonePath)
	}
}

func TestUpdateUnit(t *testing.T) {
	u := gitopsagent.UpdateServiceUnit
	if !strings.Contains(u, "\nExecStart=/usr/local/bin/gitops-agent update\n") {
		t.Error("update unit does not run `gitops-agent update`")
	}
	for _, bad := range []string{"GITHUB_RELEASE_LIB", "update.bash", "/usr/local/sbin", "Environment="} {
		if strings.Contains(u, bad) {
			t.Errorf("update unit contains %q", bad)
		}
	}
}
