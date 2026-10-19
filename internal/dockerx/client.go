// Package dockerx is the Hub's Docker life: a thin wrapper over the `docker`
// and `docker compose` CLIs (no SDK), the recipe install/launch/stop/remove
// operations, the in-memory readiness/pending state, and the installed_recipes
// bookkeeping. Every name it creates is scoped by cfg.Project so a test Hub
// can never touch a production Hub's containers, volumes or images.
package dockerx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ansiRE mirrors the upstream escape-sequence stripper used while streaming
// build output.
var ansiRE = regexp.MustCompile("\x1b\\[[^A-Za-z]*[A-Za-z]")

// CLIError carries the command and its stderr so a failed docker call always
// explains itself in the returned error text.
type CLIError struct {
	Args   []string
	Code   int
	Stderr string
	Err    error
}

func (e *CLIError) Error() string {
	cmd := "docker " + strings.Join(e.Args, " ")
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", cmd, e.Err)
	}
	if s := strings.TrimSpace(e.Stderr); s != "" {
		return fmt.Sprintf("%s: exit %d: %s", cmd, e.Code, s)
	}
	return fmt.Sprintf("%s: exit %d", cmd, e.Code)
}

func (e *CLIError) Unwrap() error { return e.Err }

// ContainerInfo is the wire shape of GET /api/containers, matching
// daemon/models/container.py.
type ContainerInfo struct {
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Image      string         `json:"image"`
	Ports      map[string]int `json:"ports"`
	CPUPercent float64        `json:"cpu_percent"`
	MemoryMB   float64        `json:"memory_mb"`
}

// Client runs the docker CLI. Bin defaults to "docker"; tests point PATH at a
// fake script instead of overriding it.
type Client struct {
	Bin string
	Log *slog.Logger
}

// NewClient returns a Client that shells out to the docker CLI.
func NewClient() *Client { return &Client{Bin: "docker", Log: slog.Default()} }

func (c *Client) bin() string {
	if c == nil || c.Bin == "" {
		return "docker"
	}
	return c.Bin
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, c.bin(), args...)
}

// Output runs a docker command and returns stdout, stderr and the exit code.
// A code of -1 means the process could not be started at all (e.g. no docker
// in PATH), which callers treat the same as a failure.
func (c *Client) Output(ctx context.Context, args ...string) (stdout, stderr string, code int) {
	cmd := c.command(ctx, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return out.String(), errb.String(), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errb.String(), ee.ExitCode()
	}
	return out.String(), errb.String(), -1
}

// Run is Output with non-zero exits turned into a CLIError carrying stderr.
func (c *Client) Run(ctx context.Context, args ...string) (string, error) {
	out, errb, code := c.Output(ctx, args...)
	if code != 0 {
		return out, &CLIError{Args: args, Code: code, Stderr: errb}
	}
	return out, nil
}

// Inspect runs `docker inspect --format` against one or more names and returns
// one output line per object.
func (c *Client) Inspect(ctx context.Context, format string, names ...string) ([]string, error) {
	args := append([]string{"inspect", "--format", format}, names...)
	out, err := c.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// List returns the compose-labelled containers, exactly as /api/containers
// shows them. A failing docker call yields an empty list, never an error.
func (c *Client) List(ctx context.Context) []ContainerInfo {
	out, _, code := c.Output(ctx, "ps", "--filter", "label=com.docker.compose.project",
		"--format", "{{.Names}}\t{{.Status}}\t{{.Image}}\t{{.Ports}}")
	if code != 0 {
		return []ContainerInfo{}
	}
	containers := []ContainerInfo{}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		info := ContainerInfo{Ports: map[string]int{}}
		if len(parts) > 0 {
			info.Name = parts[0]
		}
		if len(parts) > 1 {
			info.Status = parts[1]
		}
		if len(parts) > 2 {
			info.Image = parts[2]
		}
		if len(parts) > 3 {
			info.Ports = ParsePorts(parts[3])
		}
		containers = append(containers, info)
	}
	return containers
}

// RunContainer starts a detached container (`docker run ...`) and returns its
// stdout. It is exposed for completeness; the Hub itself uses compose.
func (c *Client) RunContainer(ctx context.Context, args ...string) (string, error) {
	return c.Run(ctx, append([]string{"run"}, args...)...)
}

// Logs returns a container's logs with stderr merged into stdout. Used for
// one-shot reads (the failure tail); the log websocket streams via Stream.
func (c *Client) Logs(ctx context.Context, container string, follow bool, tail int) (string, int) {
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	if tail > 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	args = append(args, container)
	out, _, code := c.Output(ctx, args...)
	return out, code
}

// StopContainer issues `docker stop` for the named containers.
func (c *Client) StopContainer(ctx context.Context, names ...string) error {
	if len(names) == 0 {
		return nil
	}
	_, err := c.Run(ctx, append([]string{"stop"}, names...)...)
	return err
}

// RM removes containers (`docker rm`); args may carry -f.
func (c *Client) RM(ctx context.Context, args ...string) error {
	_, err := c.Run(ctx, append([]string{"rm"}, args...)...)
	return err
}

// RMI removes images (`docker rmi`), returning stderr for the caller to
// classify ("No such image" is not an error).
func (c *Client) RMI(ctx context.Context, args ...string) (string, int) {
	_, errb, code := c.Output(ctx, append([]string{"rmi"}, args...)...)
	return errb, code
}

// VolumeRM removes volumes (`docker volume rm`), returning stderr and code.
func (c *Client) VolumeRM(ctx context.Context, args ...string) (string, int) {
	_, errb, code := c.Output(ctx, append([]string{"volume", "rm"}, args...)...)
	return errb, code
}

// ComposeArgs builds the shared `docker compose -p <project> -f <file>` prefix.
func (c *Client) ComposeArgs(project, composeFile string) []string {
	return []string{"compose", "-p", project, "-f", composeFile}
}

// ComposeBuild / ComposePull / ComposeUp / ComposeDown are the compose verbs
// used by the recipe lifecycle.
func (c *Client) ComposeBuild(ctx context.Context, project, composeFile string) (string, error) {
	return c.Run(ctx, append(c.ComposeArgs(project, composeFile), "build")...)
}

func (c *Client) ComposePull(ctx context.Context, project, composeFile string) (string, error) {
	return c.Run(ctx, append(c.ComposeArgs(project, composeFile), "pull")...)
}

func (c *Client) ComposeUp(ctx context.Context, project, composeFile string, build bool) (string, error) {
	args := append(c.ComposeArgs(project, composeFile), "up", "-d")
	if build {
		args = append(args, "--build")
	}
	return c.Run(ctx, args...)
}

func (c *Client) ComposeDown(ctx context.Context, project, composeFile string, volumes bool) (string, error) {
	args := append(c.ComposeArgs(project, composeFile), "down")
	if volumes {
		args = append(args, "--volumes")
	}
	return c.Run(ctx, args...)
}

// Stream runs a command, forwarding each non-empty output line (stdout and
// stderr merged) to onLine, and returns the exit code. When stripANSI is set
// the escape sequences are removed first, matching the install/update stream.
func (c *Client) Stream(ctx context.Context, dir string, env []string, stripANSI bool,
	onLine func(string), args ...string) (int, error) {
	cmd := c.command(ctx, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	// The open file from StdoutPipe is reused for stderr so the two streams
	// interleave in one pipe, like upstream's STDOUT redirect.
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	sc.Split(splitAnyNewline)
	for sc.Scan() {
		line := sc.Text()
		if stripANSI {
			line = ansiRE.ReplaceAllString(line, "")
		}
		line = strings.TrimSpace(line)
		if line != "" && onLine != nil {
			onLine(line)
		}
	}
	_ = sc.Err()
	waitErr := cmd.Wait()
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, waitErr
	}
	return 0, nil
}

// splitAnyNewline splits on \n and \r so carriage-return progress bars are
// yielded as separate tokens instead of piling up in one line.
func splitAnyNewline(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ParsePorts turns the `{{.Ports}}` column into {container_port: host_port},
// keeping only published mappings ("7860/tcp" without a host port is dropped),
// exactly like upstream's _parse_ports.
func ParsePorts(portsStr string) map[string]int {
	ports := map[string]int{}
	if portsStr == "" {
		return ports
	}
	for _, mapping := range strings.Split(portsStr, ", ") {
		if !strings.Contains(mapping, "->") {
			continue
		}
		hostPart, containerPart, _ := strings.Cut(mapping, "->")
		idx := strings.LastIndex(hostPart, ":")
		if idx < 0 {
			continue
		}
		hostPort, err := strconv.Atoi(strings.TrimSpace(hostPart[idx+1:]))
		if err != nil {
			continue
		}
		containerPort := strings.SplitN(containerPart, "/", 2)[0]
		ports[containerPort] = hostPort
	}
	return ports
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
