package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"go.kenn.io/kit/backup"
	"go.kenn.io/msgvault/internal/backupapp"
)

// Retention uses Kit's repository locks, dependency validation and durable pack
// publication. No code here selects or removes individual content-addressed files.
func newBackupForgetCommand() *cobra.Command {
	var repo string
	var dry bool
	c := &cobra.Command{Use: "forget SNAPSHOT...", Short: "Remove selected recovery points without deleting referenced content", Args: cobra.MinimumNArgs(1)}
	c.Flags().StringVar(&repo, "repo", "", "Backup repository")
	c.Flags().BoolVar(&dry, "dry-run", false, "Report the selection without removing recovery points")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		r, err := openRetentionRepo(cmd, repo)
		if err != nil {
			return err
		}
		res, err := backup.Forget(cmd.Context(), r, backup.ForgetOptions{SnapshotIDs: args, DryRun: dry})
		if err != nil {
			return fmt.Errorf("forgetting snapshots: %w", err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Selected %d recovery points; removed %d. Referenced content is retained until backup prune.\n", len(res.Selected), len(res.Forgotten))
		if err != nil {
			return fmt.Errorf("write backup forget output: %w", err)
		}
		return nil
	}
	return c
}

func newBackupPruneCommand() *cobra.Command {
	var repo string
	var dry bool
	c := &cobra.Command{Use: "prune", Short: "Reclaim content no retained recovery point needs", Args: cobra.NoArgs}
	c.Flags().StringVar(&repo, "repo", "", "Backup repository")
	c.Flags().BoolVar(&dry, "dry-run", false, "Report reclaimable content without changing the repository")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		r, err := openRetentionRepo(cmd, repo)
		if err != nil {
			return err
		}
		return pruneBackupRepository(cmd, r, dry)
	}
	return c
}

func newBackupRetainCommand() *cobra.Command {
	var repo string
	var dry bool
	var keep int
	var protect []string
	c := &cobra.Command{Use: "retain", Short: "Keep recent snapshots, protected recovery anchors and their dependencies", Args: cobra.NoArgs}
	c.Flags().StringVar(&repo, "repo", "", "Backup repository")
	c.Flags().BoolVar(&dry, "dry-run", false, "Report the selection without changing the repository")
	c.Flags().IntVar(&keep, "keep-last", 0, "Number of newest snapshots to retain (required, at least one)")
	c.Flags().StringSliceVar(&protect, "protect", nil, "Also retain these independently qualified recovery anchors (repeatable)")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		if keep < 1 {
			return errors.New("backup retain: --keep-last must be at least one")
		}
		r, err := openRetentionRepo(cmd, repo)
		if err != nil {
			return err
		}
		snapshots, err := r.ListSnapshots()
		if err != nil {
			return fmt.Errorf("list backup snapshots: %w", err)
		}
		selected, err := retentionSelection(snapshots, keep, protect)
		if err != nil {
			return err
		}
		for _, id := range selected {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Obsolete snapshot: %s\n", id); err != nil {
				return fmt.Errorf("write backup retention selection: %w", err)
			}
		}
		if len(selected) > 0 {
			_, err = backup.Forget(cmd.Context(), r, backup.ForgetOptions{SnapshotIDs: selected, DryRun: dry})
			if err != nil {
				return fmt.Errorf("retaining snapshots: %w", err)
			}
		}
		if _, err = fmt.Fprintf(cmd.OutOrStdout(), "Selected %d obsolete recovery points; retaining newest %d plus protected anchors and dependencies.\n", len(selected), keep); err != nil {
			return fmt.Errorf("write backup retention summary: %w", err)
		}
		// A dry-run must not pretend that manifests selected above have vanished.
		// Run prune separately after applying to see the resulting space estimate.
		if dry {
			return nil
		}
		return pruneBackupRepository(cmd, r, false)
	}
	return c
}

func openRetentionRepo(cmd *cobra.Command, flag string) (*backup.Repo, error) {
	state := invocationFromCommand(cmd)
	if state == nil || state.cfg == nil {
		return nil, errors.New("configuration is unavailable")
	}
	path, err := resolveBackupRepo(flag, state.cfg)
	if err != nil {
		return nil, err
	}
	r, err := backup.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening backup repository: %w", err)
	}
	return r, nil
}

func pruneBackupRepository(cmd *cobra.Command, r *backup.Repo, dry bool) error {
	res, err := backup.Prune(cmd.Context(), r, backupapp.New(Version), backup.PruneOptions{DryRun: dry})
	if err != nil {
		return fmt.Errorf("pruning backup repository: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Packs selected: %d; planned pack bytes: %d; bytes removed: %d; replacement bytes written: %d.\n", len(res.PacksToRemove), res.BytesToRemove, res.BytesRemoved, res.BytesWritten)
	if err != nil {
		return fmt.Errorf("write backup prune output: %w", err)
	}
	return nil
}

func retentionSelection(snapshots []*backup.Manifest, keep int, protected []string) ([]string, error) {
	byID := make(map[string]*backup.Manifest, len(snapshots))
	retained := map[string]bool{}
	for _, m := range snapshots {
		byID[m.SnapshotID] = m
	}
	var retain func(string) error
	retain = func(id string) error {
		if retained[id] {
			return nil
		}
		m, ok := byID[id]
		if !ok {
			return fmt.Errorf("backup retain: recovery point %q not found", id)
		}
		retained[id] = true
		// ParentID of a keyframe is informational; only incremental page maps
		// depend on their parents. Kit revalidates the full chain under its lock.
		if m.DB.MapChainDepth > 0 {
			return retain(m.ParentID)
		}
		return nil
	}
	for _, id := range protected {
		if err := retain(id); err != nil {
			return nil, err
		}
	}
	start := max(0, len(snapshots)-keep)
	for _, m := range snapshots[start:] {
		if err := retain(m.SnapshotID); err != nil {
			return nil, err
		}
	}
	var selected []string
	for _, m := range snapshots {
		if !retained[m.SnapshotID] {
			selected = append(selected, m.SnapshotID)
		}
	}
	return selected, nil
}

func init() {
	backupCmd.AddCommand(newBackupForgetCommand(), newBackupPruneCommand(), newBackupRetainCommand())
}
