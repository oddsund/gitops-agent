package cmdrun

import "context"

// Call is one recorded invocation of a Fake.
type Call struct {
	Env  []string
	Name string
	Args []string
}

// Fake records calls and answers via Func (nil Func means success with no
// output). It lives outside a _test file so tests in other packages can use
// it. It is not safe for concurrent use.
type Fake struct {
	Calls []Call
	Func  func(Call) ([]byte, error)
}

func (f *Fake) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	call := Call{
		Env:  append([]string(nil), env...),
		Name: name,
		Args: append([]string(nil), args...),
	}
	f.Calls = append(f.Calls, call)
	if f.Func == nil {
		return nil, nil
	}
	return f.Func(call)
}
