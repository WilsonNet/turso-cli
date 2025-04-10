package cmd

import "github.com/spf13/cobra"

var locationFlag string

func addLocationFlag(cmd *cobra.Command, desc string) {
	cmd.Flags().StringVar(&locationFlag, "location", "", desc)
	cmd.RegisterFlagCompletionFunc("location", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		client, err := createTursoClient()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return getRegionIds(client), cobra.ShellCompDirectiveNoFileComp
	})
}
