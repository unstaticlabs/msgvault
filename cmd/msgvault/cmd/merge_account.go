package cmd

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/accountops"
	"go.kenn.io/msgvault/internal/store"
)

func newMergeAccountCmd() *cobra.Command {
	var request accountops.MergeRequest
	var jsonOutput bool
	var confirmed bool
	cmd := &cobra.Command{
		Use:   "merge-account --from <account> --into <account>",
		Short: "Merge archived accounts",
		Long:  "Move archived messages into a compatible account, preserve original evidence, and hide only unambiguous duplicates. Use --dry-run to preview the checks, counts and who gains or loses access to the moved history. A merge retires the historical source, so preview first, then repeat with --yes to apply it.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			request.DryRunSet = true
			request.FromSourceIDSet = cmd.Flags().Changed("from-source-id")
			request.IntoSourceIDSet = cmd.Flags().Changed("into-source-id")
			if !request.DryRun && !confirmed {
				return errors.New("merge retires the historical source; preview with --dry-run, then repeat with --yes to apply")
			}
			st, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			result, err := st.MergeCLIAccounts(cmd.Context(), request)
			if err != nil {
				return err
			}
			if jsonOutput {
				data, err := json.Marshal(result)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
				if err != nil {
					return fmt.Errorf("write merge report: %w", err)
				}
				return nil
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Source %d into %d: %d messages moved, %d duplicates hidden, %d ambiguous matches preserved (dry-run: %t, already merged: %t)\n", result.FromSourceID, result.IntoSourceID, result.MessagesMoved, result.DuplicatesHidden, result.AmbiguousMatches, result.DryRun, result.AlreadyMerged)
			if err != nil {
				return fmt.Errorf("write merge report: %w", err)
			}
			return writeSourceMergeAccessImpact(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVar(&request.From, "from", "", "Historical account identifier or alias")
	cmd.Flags().StringVar(&request.Into, "into", "", "Destination account identifier or alias")
	cmd.Flags().Int64Var(&request.FromSourceID, "from-source-id", 0, "Exact historical source ID")
	cmd.Flags().Int64Var(&request.IntoSourceID, "into-source-id", 0, "Exact destination source ID")
	cmd.Flags().BoolVar(&request.DryRun, "dry-run", false, "Preview without changing the archive")
	cmd.Flags().BoolVar(&confirmed, "yes", false, "Confirm retirement of the historical source")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the merge report as JSON")
	return cmd
}

func init() { rootCmd.AddCommand(newMergeAccountCmd()) }

func writeSourceMergeAccessImpact(out io.Writer, result *store.SourceMergeResult) error {
	impact := result.AccessImpact
	if impact == nil {
		_, err := fmt.Fprintln(out, "Access changes were not recorded in this older report. Moved history follows destination access; source assignments are not copied.")
		if err != nil {
			return fmt.Errorf("write historical access context: %w", err)
		}
		return nil
	}
	if result.AlreadyMerged {
		if _, err := fmt.Fprintf(out, "Recorded source access impact (%s); current assignments may have changed.\n", impact.AssessedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
			return fmt.Errorf("write recorded access context: %w", err)
		}
	} else if _, err := fmt.Fprintf(out, "Source access impact at %s (current user-account roles and assignments):\n", impact.AssessedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
		return fmt.Errorf("write access assessment time: %w", err)
	}
	if _, err := fmt.Fprintln(out, "Moved history follows destination source access."); err != nil {
		return fmt.Errorf("write merge access context: %w", err)
	}
	for _, group := range []struct {
		label string
		users []store.SourceMergeAccessUser
	}{{"Gains source access to moved history", impact.GainingUsers}, {"Loses source access to moved history", impact.LosingUsers}} {
		if len(group.users) == 0 {
			if _, err := fmt.Fprintf(out, "%s: none.\n", group.label); err != nil {
				return fmt.Errorf("write merge access users: %w", err)
			}
		}
		for _, user := range group.users {
			if _, err := fmt.Fprintf(out, "%s: %s (user %d, %s).\n", group.label, user.Email, user.UserID, user.Role); err != nil {
				return fmt.Errorf("write merge access user: %w", err)
			}
		}
	}
	_, err := fmt.Fprintln(out, "Access to existing destination history is unchanged. Source assignments are not copied. Administrators keep all-source access; disabled users remain disabled. Native message rules and each credential's configured role still apply.")
	if err != nil {
		return fmt.Errorf("write merge access consequences: %w", err)
	}
	return nil
}
