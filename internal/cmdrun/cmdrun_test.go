package cmdrun

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestReal_RunsWithoutShell(t *testing.T) {
	out, err := Real{}.Run(t.Context(), nil, "/bin/echo", "a;b", "$HOME")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a;b $HOME\n" {
		t.Fatalf("got %q", out)
	}
}

func TestReal_Env(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	out, err := Real{}.Run(t.Context(), []string{"FOO=bar"}, "/bin/sh", "-c", `printf %s "$FOO"`)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "bar" {
		t.Fatalf("got %q", out)
	}
}

func TestReal_GrandchildHoldingPipeDoesNotBlock(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	Real{}.Run(ctx, nil, "/bin/sh", "-c", "sleep 30 & echo started; wait")
	if elapsed := time.Since(start); elapsed > waitDelay+3*time.Second {
		t.Fatalf("Run took %v, want it to return shortly after the context deadline", elapsed)
	}
}

func TestReal_Error(t *testing.T) {
	if _, err := os.Stat("/bin/false"); err != nil {
		t.Skip("no /bin/false")
	}
	if _, err := (Real{}).Run(t.Context(), nil, "/bin/false"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFake_Records(t *testing.T) {
	f := &Fake{}
	env := []string{"A=b"}
	if out, err := f.Run(t.Context(), env, "x", "1", "2"); out != nil || err != nil {
		t.Fatalf("nil Func should succeed silently, got %q, %v", out, err)
	}
	env[0] = "mutated"
	want := []Call{{Env: []string{"A=b"}, Name: "x", Args: []string{"1", "2"}}}
	if !reflect.DeepEqual(f.Calls, want) {
		t.Fatalf("calls = %+v, want %+v", f.Calls, want)
	}
}
