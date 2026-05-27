package main

import (
	_ "embed"
	"fmt"

	"github.com/spf13/cobra"
)

//go:embed embed/ai-help.md
var aiHelpText string

// newAIHelpCmd prints an embedded cheat sheet suitable for piping into
// an AI agent's system prompt. No network, no state — just static text
// shipped with the binary so a fresh install can teach an LLM the
// command surface.
func newAIHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ai-help",
		Short: "Print AI cheat sheet (suitable for AI system prompt)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprint(cmd.OutOrStdout(), aiHelpText)
			return nil
		},
	}
}
