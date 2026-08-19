package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/registry"
	"github.com/RefireLab/gh-runnerd/internal/units"
)

func imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage local container images in the embedded registry",
	}
	cmd.AddCommand(imageListCmd())
	cmd.AddCommand(imagePullCmd())
	cmd.AddCommand(imageImportCmd())
	cmd.AddCommand(imageAddCmd())
	cmd.AddCommand(imageInspectCmd())
	cmd.AddCommand(imageRemoveCmd())
	cmd.AddCommand(imagePruneCmd())
	return cmd
}

func pinnedStore(cmd *cobra.Command) (*registry.Store, error) {
	cfg, err := loadConfigOptional(cmd)
	if err != nil {
		return nil, err
	}
	if err := cfg.Layout().Ensure(); err != nil {
		return nil, err
	}
	return registry.OpenPinned(cfg.Layout(), cfg.PinnedQuotaBytes()), nil
}

func cacheStore(cmd *cobra.Command) (*registry.Store, error) {
	cfg, err := loadConfigOptional(cmd)
	if err != nil {
		return nil, err
	}
	if err := cfg.Layout().Ensure(); err != nil {
		return nil, err
	}
	return registry.OpenCache(cfg.Layout(), cfg.CacheQuotaBytes()), nil
}

func imageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List pinned container images",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := pinnedStore(cmd)
			if err != nil {
				return err
			}
			list, err := s.List("")
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no images)")
				return nil
			}
			for _, rec := range list {
				fmt.Fprintf(cmd.OutOrStdout(), "%s:%s\t%s\tgh-runnerd.local/%s:%s\n", rec.Name, rec.Tag, rec.Digest, rec.Name, rec.Tag)
			}
			return nil
		},
	}
}

func imagePullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <image>",
		Short: "Pull an image from Docker Hub/GHCR into the local registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := pinnedStore(cmd)
			if err != nil {
				return err
			}
			cfg, _ := loadConfigOptional(cmd)
			parsed, err := registry.ParseLocalRef(args[0])
			if err != nil {
				return err
			}
			res, err := s.Pull(context.Background(), args[0], parsed.Name, parsed.Tag, "", registry.UpstreamAuth{
				DockerHubUser:  cfg.Registry.DockerHubUsername,
				DockerHubToken: cfg.Registry.DockerHubToken,
			})
			if err != nil {
				return err
			}
			printImport(cmd.OutOrStdout(), res.Name, res.Tag, res.Digest, res.Reference)
			return nil
		},
	}
}

func imageImportCmd() *cobra.Command {
	var name, tag string
	cmd := &cobra.Command{
		Use:   "import <tar>",
		Short: "Import a docker save or OCI archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || tag == "" {
				return fmt.Errorf("--name and --tag are required")
			}
			s, err := pinnedStore(cmd)
			if err != nil {
				return err
			}
			res, err := s.ImportTar(args[0], name, tag, "")
			if err != nil {
				return err
			}
			printImport(cmd.OutOrStdout(), res.Name, res.Tag, res.Digest, res.Reference)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "local image name")
	cmd.Flags().StringVar(&tag, "tag", "", "local image tag")
	return cmd
}

func imageAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <remote>",
		Short: "Pull a remote image and tag it locally (alias helper)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := pinnedStore(cmd)
			if err != nil {
				return err
			}
			cfg, _ := loadConfigOptional(cmd)
			local, err := registry.ParseLocalRef(args[0])
			if err != nil {
				return err
			}
			res, err := s.Pull(context.Background(), args[1], local.Name, local.Tag, "", registry.UpstreamAuth{
				DockerHubUser:  cfg.Registry.DockerHubUsername,
				DockerHubToken: cfg.Registry.DockerHubToken,
			})
			if err != nil {
				return err
			}
			printImport(cmd.OutOrStdout(), res.Name, res.Tag, res.Digest, res.Reference)
			return nil
		},
	}
}

func imageInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <name:tag>",
		Short: "Inspect a pinned image and verify its digest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := pinnedStore(cmd)
			if err != nil {
				return err
			}
			p, err := registry.ParseLocalRef(args[0])
			if err != nil {
				return err
			}
			insp, err := s.Inspect(p.Name, p.Tag, "")
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Name:       %s\n", insp.Name)
			fmt.Fprintf(cmd.OutOrStdout(), "Tag:        %s\n", insp.Tag)
			fmt.Fprintf(cmd.OutOrStdout(), "Digest:     %s\n", insp.Digest)
			fmt.Fprintf(cmd.OutOrStdout(), "Reference:  %s\n", insp.Reference)
			fmt.Fprintf(cmd.OutOrStdout(), "Digest OK:  %v\n", insp.DigestOK)
			return nil
		},
	}
}

func imageRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name:tag>",
		Short: "Untag a pinned image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := pinnedStore(cmd)
			if err != nil {
				return err
			}
			p, err := registry.ParseLocalRef(args[0])
			if err != nil {
				return err
			}
			return s.Remove(p.Name, p.Tag, "")
		},
	}
}

func imagePruneCmd() *cobra.Command {
	var dry bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete unreferenced blobs from pinned and cache stores",
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, err := pinnedStore(cmd)
			if err != nil {
				return err
			}
			cache, err := cacheStore(cmd)
			if err != nil {
				return err
			}
			for _, pair := range []struct {
				name  string
				store *registry.Store
			}{{"pinned", pinned}, {"cache", cache}} {
				removed, freed, err := pair.store.Prune(dry)
				if err != nil {
					return err
				}
				mode := "deleted"
				if dry {
					mode = "would delete"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s %d blobs (%s)\n", pair.name, mode, len(removed), units.FormatBytes(freed))
				for _, d := range removed {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", d)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "report blobs without deleting them")
	return cmd
}
