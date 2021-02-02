package command

import (
	"flag"
	"fmt"
	"strings"

	"github.com/mitchellh/cli"
	"github.com/posener/complete"
)

var _ cli.Command = (*OperatorRaftAutopilotHealthCommand)(nil)
var _ cli.CommandAutocomplete = (*OperatorRaftAutopilotHealthCommand)(nil)

type OperatorRaftAutopilotHealthCommand struct {
	*BaseCommand
}

func (c *OperatorRaftAutopilotHealthCommand) Synopsis() string {
	return "Displays current autopilot state"
}

func (c *OperatorRaftAutopilotHealthCommand) Help() string {
	helpText := `
Usage: vault operator raft autopilot state

  Displays current autopilot state.
` + c.Flags().Help()

	return strings.TrimSpace(helpText)
}

func (c *OperatorRaftAutopilotHealthCommand) Flags() *FlagSets {
	set := c.flagSet(FlagSetHTTP | FlagSetOutputFormat)
	set.mainSet.VisitAll(func(fl *flag.Flag) {
		if fl.Name == "format" {
			fl.DefValue = "pretty"
		}
	})
	ui, ok := c.UI.(*VaultUI)
	if ok && ui.format == "table" {
		ui.format = "pretty"
	}
	return set
}

func (c *OperatorRaftAutopilotHealthCommand) AutocompleteArgs() complete.Predictor {
	return complete.PredictAnything
}

func (c *OperatorRaftAutopilotHealthCommand) AutocompleteFlags() complete.Flags {
	return c.Flags().Completions()
}

func (c *OperatorRaftAutopilotHealthCommand) Run(args []string) int {
	f := c.Flags()

	if err := f.Parse(args); err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	args = f.Args()
	switch len(args) {
	case 0:
	default:
		c.UI.Error(fmt.Sprintf("Incorrect arguments (expected 0, got %d)", len(args)))
		return 1
	}

	client, err := c.Client()
	if err != nil {
		c.UI.Error(err.Error())
		return 2
	}

	state, err := client.Sys().RaftAutopilotHealth()
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error checking autopilot health: %s", err))
		return 2
	}

	return OutputData(c.UI, state)
}
