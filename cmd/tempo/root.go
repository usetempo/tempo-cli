package tempo

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/usetempo/tempo-cli/internal/config"
	"github.com/usetempo/tempo-cli/internal/detector"
	"github.com/usetempo/tempo-cli/internal/hooks"
	"github.com/usetempo/tempo-cli/internal/sender"
)

var cliVersion string

// Execute runs the tempo-cli root command with all subcommands registered.
func Execute(version string) error {
	cliVersion = version

	rootCmd := &cobra.Command{
		Use:     "tempo-cli",
		Short:   "AI code attribution for git commits",
		Version: version,
	}

	rootCmd.AddCommand(
		newEnableCmd(),
		newDisableCmd(),
		newAuthCmd(),
		newStatusCmd(),
		newTestCmd(),
		newDetectCmd(),
		newSyncCmd(),
	)

	return rootCmd.Execute()
}

func newEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Install git hooks for AI attribution detection",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitRepoRoot()
			if err != nil {
				return fmt.Errorf("not a git repository (run this inside a git repo)")
			}
			if err := hooks.Install(repoRoot); err != nil {
				return fmt.Errorf("installing hooks: %w", err)
			}
			fmt.Println("Tempo hooks installed successfully.")

			cfg, _ := config.Load()
			if cfg.APIToken == "" {
				fmt.Println("Warning: No API token configured. Running in offline mode.")
				fmt.Println("Run 'tempo-cli auth <token>' to connect to Tempo cloud.")
			}
			return nil
		},
	}
}

func newDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Remove Tempo git hooks",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitRepoRoot()
			if err != nil {
				return fmt.Errorf("not a git repository")
			}
			if err := hooks.Uninstall(repoRoot); err != nil {
				return fmt.Errorf("removing hooks: %w", err)
			}
			fmt.Println("Tempo hooks removed.")
			return nil
		},
	}
}

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth <token>",
		Short: "Configure API token for Tempo cloud",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token := args[0]
			endpoint, _ := cmd.Flags().GetString("endpoint")

			cfg, err := config.Load()
			if err != nil {
				cfg = &config.Config{}
			}
			cfg.APIToken = token
			if endpoint != "" {
				cfg.Endpoint = endpoint
			}

			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
			fmt.Println("API token saved.")
			return nil
		},
	}
	cmd.Flags().String("endpoint", "", "API endpoint override")
	return cmd
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Tempo CLI status",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitRepoRoot()
			if err != nil {
				return fmt.Errorf("not a git repository")
			}

			// Hooks
			if hooks.IsInstalled(repoRoot) {
				fmt.Println("Hooks:     installed")
			} else {
				fmt.Println("Hooks:     not installed")
			}

			// Pending
			pending := sender.PendingCount(repoRoot)
			fmt.Printf("Pending:   %d attribution records\n", pending)

			// API token
			cfg, _ := config.Load()
			if cfg.APIToken != "" {
				fmt.Println("API token: configured")
			} else {
				fmt.Println("API token: not configured (offline mode)")
			}

			return nil
		},
	}
}

func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Dry-run detection against the last commit",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitRepoRoot()
			if err != nil {
				return fmt.Errorf("not a git repository")
			}

			attr, err := detector.Detect(repoRoot)
			if err != nil {
				return err
			}
			if attr == nil {
				fmt.Println("No AI tool usage detected in the last commit.")
				return nil
			}

			jsonFlag, _ := cmd.Flags().GetBool("json")
			if jsonFlag {
				data, _ := json.MarshalIndent(attr, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			// Human-readable output
			fmt.Printf("Commit:  %s\n", attr.CommitSHA)
			fmt.Printf("Author:  %s\n", attr.CommitAuthor)
			if attr.Repo != "" {
				fmt.Printf("Repo:    %s\n", attr.Repo)
			}
			fmt.Println()

			for _, d := range attr.Detections {
				icon := "\U0001f7e2" // green circle
				if d.Confidence == detector.ConfidenceMedium {
					icon = "\U0001f7e1" // yellow circle
				}
				fmt.Printf("%s  %s (%s confidence, %s)\n", icon, d.Tool, d.Confidence, d.Method)

				if len(d.FilesMatched) > 0 {
					fmt.Printf("   Files: %d/%d committed files matched\n", d.AIFiles, d.FilesCommitted)
					for _, f := range d.FilesMatched {
						fmt.Printf("     - %s\n", f)
					}
				}
				if d.Model != "" {
					fmt.Printf("   Model: %s\n", d.Model)
				}
				if d.TokenUsage > 0 {
					fmt.Printf("   Tokens: %d\n", d.TokenUsage)
				}
				if d.SessionDurationSec > 0 {
					mins := d.SessionDurationSec / 60
					secs := d.SessionDurationSec % 60
					fmt.Printf("   Session: %dm%ds\n", mins, secs)
				}
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Output in JSON format")
	return cmd
}

func newDetectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "_detect",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitRepoRoot()
			if err != nil {
				return nil
			}
			attr, err := detector.Detect(repoRoot)
			if err != nil || attr == nil {
				return nil
			}
			return sender.SavePending(repoRoot, attr)
		},
	}
	cmd.Flags().String("hook", "", "hook type (internal)")
	return cmd
}

func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_sync",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitRepoRoot()
			if err != nil {
				return nil
			}
			return sender.Sync(repoRoot, cliVersion)
		},
	}
}

func gitRepoRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
