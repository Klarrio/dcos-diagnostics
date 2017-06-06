package exec

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/pkg/errors"
)

// dcos-go/exec is a os/exec wrapper. It implements io.Reader and can be used to read both STDOUT and STDERR.
//
// Usage:
// ce := exec.Run("bash", []string{"infinite.sh"}, exec.Timeout(3 * time.Second))
//
// io.Copy(os.Stdout, ce)
// err := <- ce.Done
// if err != nil {
// 	log.Error(err)
// }

// CommandExecutor is a structure returned by exec.Run
// Cancel can be used by a user to interrupt a command execution.
// Done is a channel the user can read in order to retrieve execution status. Possible statuses:
//  <nil> command executed successfully, returned 0 exit code
//  <exit status N> where N is non 0 exit status.
//  <context deadline exceeded> means timeout was reached and command was killed.
//  <context canceled>  means that command was canceled by a user.
type CommandExecutor struct {
	Done chan error

	done chan error
	pipe *io.PipeReader
}

// Read implements the io.Reader.
// CommandExecutor will read from stdout and stderr
func (c *CommandExecutor) Read(p []byte) (int, error) {
	return c.pipe.Read(p)
}

// Run spawns the given command and returns a handle to the running process in the form
// of a CommandExecutor.
func Run(ctx context.Context, command string, arg []string) (*CommandExecutor, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// by default Cancel is spineless unless someone configures an option to enable it
	commandExecutor := &CommandExecutor{Done: make(chan error, 1), done: make(chan error, 1)}

	cmd := exec.CommandContext(ctx, command, arg...)
	go func() {
		var err error
		defer func() { commandExecutor.Done <- err }()

		select {
		case <-ctx.Done():
			err = ctx.Err()
		case err = <-commandExecutor.done:
		}
	}()

	// Create a new PIPE.
	// stdout and stderr will be both redirected to this pipe. When the command is executed / cancelled or timeout
	// reached the pipe will be closed, unblocking the reader.
	r, w := io.Pipe()
	cmd.Stdout = w
	cmd.Stderr = w
	commandExecutor.pipe = r

	// execute the command in the goroutine.
	go func() {
		defer w.Close()
		commandExecutor.done <- cmd.Run()
	}()

	return commandExecutor, nil
}

// Output returns stdout, stderr, exit code and error status for a given shell command
func Output(ctx context.Context, command ...string) (stdout []byte, stderr []byte, code int, err error) {

	var (
		// define an empty cancel function
		cancel         context.CancelFunc = func() {}
		arg            []string
		outbuf, errbuf bytes.Buffer
	)

	if len(command) == 0 {
		return nil, nil, 0, errors.New("unable to execute a command with empty Cmd field")
	}

	if ctx == nil {
		// default to 10 seconds timeout.
		ctx, cancel = context.WithTimeout(context.Background(), time.Second*10)
	}

	defer cancel()

	if len(command) > 1 {
		arg = command[1:]
	}

	cmd := exec.CommandContext(ctx, command[0], arg...)
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	if err := cmd.Start(); err != nil {
		return nil, nil, 0, err
	}

	err = cmd.Wait()
	code, err = exitCode(err)
	stdout = outbuf.Bytes()
	stderr = errbuf.Bytes()

	return stdout, stderr, code, err
}

// exitCode takes an error and checks if it's a read error or program had non zero exit code.
// The output is the return value and error. The return value must be treated as a real return code value
// only if error is nil. If error is not nil, it means it's a real error.
func exitCode(e error) (int, error) {
	if e == nil {
		return 0, nil
	}

	// check if error contains program exit code
	if exiterr, ok := e.(*exec.ExitError); ok {
		if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
			// when a program exceeded timeout it will be terminated
			// and the code -1 will be set.
			if status.ExitStatus() != -1 {
				return status.ExitStatus(), nil
			}
		}
	}

	return 0, e
}
