package cmd

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/spf13/cobra"
)

func newPrepareEmailWorkCommand() *cobra.Command {
	var batchSize int
	var all bool
	command := &cobra.Command{Use: "prepare-email-work SOURCE_ID", Short: "Prepare archived MIME evidence for related email rows", Long: "Prepare one resumable batch of an account's already archived MIME through the configured daemon. Missing or corrupt raw remains visible as incomplete evidence. Use --all to continue from durable checkpoints until the account is prepared. No provider is contacted. Existing Member write authority and account access are required; --local selects the local daemon.", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		sourceID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || sourceID < 1 || batchSize < 1 || batchSize > 1000 {
			return errors.New("choose a positive source ID and --batch-size from 1 to 1000")
		}
		client, _, err := OpenHTTPStore(command.Context())
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		encoder := json.NewEncoder(command.OutOrStdout())
		for {
			result, err := client.PrepareEmailWork(command.Context(), sourceID, batchSize)
			if err != nil {
				return err
			}
			if err = encoder.Encode(result); err != nil {
				return err
			}
			if result.Progress.Done || !all {
				return nil
			}
		}
	}}
	command.Flags().IntVar(&batchSize, "batch-size", 100, "Maximum archived messages per resumable batch (1–1000)")
	command.Flags().BoolVar(&all, "all", false, "Continue batches until preparation is complete")
	return command
}

func init() { rootCmd.AddCommand(newPrepareEmailWorkCommand()) }
