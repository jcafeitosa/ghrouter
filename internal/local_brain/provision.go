package local_brain

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ProvisionRunner is the narrow process boundary used by the explicit
// provision command. Commands are passed as argv and never through a shell.
type ProvisionRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type osProvisionRunner struct{}

func (osProvisionRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// ExecuteProvisionPlan applies only actions explicitly marked safe to apply.
// Normal startup never calls this function; installation and downloads remain
// an explicit user action through `ghrouter provision --apply`.
func ExecuteProvisionPlan(ctx context.Context, actions []ProvisionAction, runner ProvisionRunner) error {
	if ctx == nil {
		return fmt.Errorf("provision context is nil")
	}
	if runner == nil {
		runner = osProvisionRunner{}
	}
	for _, action := range actions {
		if !action.ApplyOK {
			continue
		}
		if err := validateProvisionCommand(action.Command); err != nil {
			return fmt.Errorf("%s for %s: %w", action.Action, action.Provider, err)
		}
		if err := runner.Run(ctx, action.Command[0], action.Command[1:]...); err != nil {
			return fmt.Errorf("%s for %s: %w", action.Action, action.Provider, err)
		}
	}
	return nil
}

func validateProvisionCommand(command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("missing command")
	}
	name := filepath.Base(command[0])
	switch name {
	case "python", "python3", "py", "brew", "winget", "hf":
		return nil
	default:
		return fmt.Errorf("command %q is not an approved provision executable", name)
	}
}
