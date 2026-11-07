package selfupdate

import (
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/oddsund/gitops-agent/internal/cmdrun"
)

func attestationCfg() (Config, *cmdrun.Fake, *[]string) {
	fake := &cmdrun.Fake{}
	var logs []string
	return Config{
		Repo:     "test/repo",
		Runner:   fake,
		Logf:     func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		LookPath: func(string) (string, error) { return "/opt/gh", nil },
	}, fake, &logs
}

func TestAttestation_Success(t *testing.T) {
	cfg, fake, logs := attestationCfg()

	if err := verifyAttestation(t.Context(), cfg, "/x/y", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %+v", fake.Calls)
	}
	c := fake.Calls[0]
	if c.Name != "/opt/gh" {
		t.Errorf("name = %q", c.Name)
	}
	want := []string{
		"attestation", "verify", "/x/y",
		"-R", "test/repo",
		"--signer-workflow", "test/repo/.github/workflows/release.yml",
		"--source-ref", "refs/tags/v1.2.3",
		"--deny-self-hosted-runners",
	}
	if !reflect.DeepEqual(c.Args, want) {
		t.Errorf("args = %v, want %v", c.Args, want)
	}
	if !strings.Contains(joined(logs), "build provenance verified") {
		t.Errorf("logs = %v", *logs)
	}
}

func TestAttestation_Failure(t *testing.T) {
	cfg, fake, _ := attestationCfg()
	fake.Func = func(cmdrun.Call) ([]byte, error) {
		return []byte("no matching attestations"), errors.New("exit status 1")
	}

	err := verifyAttestation(t.Context(), cfg, "/x/y", "v1.2.3")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"build provenance verification failed", "no matching attestations"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestAttestation_GhMissing(t *testing.T) {
	cfg, fake, logs := attestationCfg()
	cfg.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	if err := verifyAttestation(t.Context(), cfg, "/x/y", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("calls = %+v", fake.Calls)
	}
	out := joined(logs)
	if !strings.Contains(out, "gh not found on PATH") || strings.Contains(out, "GITOPS_AGENT_SKIP_ATTESTATION") {
		t.Errorf("logs = %v", *logs)
	}
}

func TestAttestation_OptOut(t *testing.T) {
	cfg, fake, logs := attestationCfg()
	cfg.SkipAttestation = true
	cfg.LookPath = func(string) (string, error) {
		t.Error("LookPath must not be called when attestation is skipped")
		return "", exec.ErrNotFound
	}

	if err := verifyAttestation(t.Context(), cfg, "/x/y", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("calls = %+v", fake.Calls)
	}
	out := joined(logs)
	if !strings.Contains(out, "GITOPS_AGENT_SKIP_ATTESTATION=1") || strings.Contains(out, "gh not found") {
		t.Errorf("logs = %v", *logs)
	}
}

func TestAttestation_TokenInEnvOnly(t *testing.T) {
	cfg, fake, _ := attestationCfg()
	cfg.Token = "tok"
	if err := verifyAttestation(t.Context(), cfg, "/x/y", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	c := fake.Calls[0]
	if !reflect.DeepEqual(c.Env, []string{"GH_TOKEN=tok"}) {
		t.Errorf("env = %v", c.Env)
	}
	for _, a := range c.Args {
		if strings.Contains(a, "tok") {
			t.Errorf("token in argv: %v", c.Args)
		}
	}

	cfg, fake, _ = attestationCfg()
	if err := verifyAttestation(t.Context(), cfg, "/x/y", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls[0].Env) != 0 {
		t.Errorf("env = %v, want empty", fake.Calls[0].Env)
	}
}

func TestAttestation_RelativePath(t *testing.T) {
	cfg, fake, _ := attestationCfg()
	if err := verifyAttestation(t.Context(), cfg, "x", "v1.2.3"); err == nil {
		t.Fatal("expected error")
	}
	if len(fake.Calls) != 0 {
		t.Errorf("calls = %+v", fake.Calls)
	}
}

func TestAttestation_ForkDerivesWorkflow(t *testing.T) {
	cfg, fake, _ := attestationCfg()
	cfg.Repo = "someone/fork"
	if err := verifyAttestation(t.Context(), cfg, "/x/y", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"attestation", "verify", "/x/y",
		"-R", "someone/fork",
		"--signer-workflow", "someone/fork/.github/workflows/release.yml",
		"--source-ref", "refs/tags/v1.2.3",
		"--deny-self-hosted-runners",
	}
	if !reflect.DeepEqual(fake.Calls[0].Args, want) {
		t.Errorf("args = %v, want %v", fake.Calls[0].Args, want)
	}
}

func TestAttestation_InvalidTag(t *testing.T) {
	for _, tag := range []string{"", "v1.2", "latest", "v1.2.3 --foo", "v1.2.3-rc1", "--owner=x"} {
		cfg, fake, _ := attestationCfg()
		if err := verifyAttestation(t.Context(), cfg, "/x/y", tag); err == nil {
			t.Errorf("tag %q: expected error", tag)
		}
		if len(fake.Calls) != 0 {
			t.Errorf("tag %q: calls = %+v", tag, fake.Calls)
		}
	}
}

func TestAttestation_InvalidTagStillSkippedWhenOptedOut(t *testing.T) {
	cfg, fake, _ := attestationCfg()
	cfg.SkipAttestation = true
	if err := verifyAttestation(t.Context(), cfg, "/x/y", ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("calls = %+v", fake.Calls)
	}
}

func TestAttestation_OldGhHint(t *testing.T) {
	cfg, fake, _ := attestationCfg()
	fake.Func = func(cmdrun.Call) ([]byte, error) {
		return []byte("unknown flag: --signer-workflow"), errors.New("exit status 1")
	}
	err := verifyAttestation(t.Context(), cfg, "/x/y", "v1.2.3")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"unknown flag: --signer-workflow", "upgrade to gh >= " + minGHVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if len(fake.Calls) != 1 {
		t.Errorf("calls = %+v, want exactly one (no retry without the flags)", fake.Calls)
	}
}
