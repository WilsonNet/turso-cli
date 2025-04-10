package cmd

import (
	"bufio"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/briandowns/spinner"
	"github.com/chiselstrike/iku-turso-cli/internal/settings"
	"github.com/chiselstrike/iku-turso-cli/internal/turso"
	"github.com/olekukonko/tablewriter"
	"golang.org/x/sync/errgroup"
)

func createTursoClient() (*turso.Client, error) {
	token, err := getAccessToken()
	if err != nil {
		return nil, err
	}
	return tursoClient(&token), nil
}

func createUnauthenticatedTursoClient() *turso.Client {
	return tursoClient(nil)
}

func tursoClient(token *string) *turso.Client {
	tursoUrl, err := url.Parse(getTursoUrl())
	if err != nil {
		log.Fatal(fmt.Errorf("error creating turso client: could not parse turso URL %s: %w", getTursoUrl(), err))
	}

	return turso.New(tursoUrl, token, &version)
}

func filterInstancesByRegion(instances []turso.Instance, region string) []turso.Instance {
	result := []turso.Instance{}
	for _, instance := range instances {
		if instance.Region == region {
			result = append(result, instance)
		}
	}
	return result
}

func extractPrimary(instances []turso.Instance) (primary *turso.Instance, others []turso.Instance) {
	result := []turso.Instance{}
	for _, instance := range instances {
		if instance.Type == "primary" {
			primary = &instance
			continue
		}
		result = append(result, instance)
	}
	return primary, result
}

func getDatabaseUrl(settings *settings.Settings, db *turso.Database, password bool) string {
	return getUrl(settings, db, nil, "libsql", password)
}

func getInstanceUrl(settings *settings.Settings, db *turso.Database, inst *turso.Instance) string {
	return getUrl(settings, db, inst, "libsql", false)
}

func getDatabaseHttpUrl(settings *settings.Settings, db *turso.Database) string {
	return getUrl(settings, db, nil, "https", true)
}

func getInstanceHttpUrl(settings *settings.Settings, db *turso.Database, inst *turso.Instance) string {
	return getUrl(settings, db, inst, "https", true)
}

func getUrl(settings *settings.Settings, db *turso.Database, inst *turso.Instance, scheme string, password bool) string {
	dbSettings := settings.GetDatabaseSettings(db.ID)
	if dbSettings == nil {
		// Backwards compatibility with old settings files.
		dbSettings = settings.GetDatabaseSettings(db.Name)
	}

	host := db.Hostname
	if inst != nil {
		host = inst.Hostname
	}

	user := ""
	if password && dbSettings != nil {
		user = fmt.Sprintf("%s:%s@", dbSettings.Username, dbSettings.Password)
	}

	return fmt.Sprintf("%s://%s%s", scheme, user, host)
}

func getDatabaseRegions(db turso.Database) string {
	regions := make([]string, 0, len(db.Regions))
	for _, region := range db.Regions {
		if region == db.PrimaryRegion {
			region = fmt.Sprintf("%s (primary)", turso.Emph(region))
		}
		regions = append(regions, region)
	}

	return strings.Join(regions, ", ")
}

func printTable(header []string, data [][]string) {
	table := tablewriter.NewWriter(os.Stdout)

	table.SetHeader(header)
	table.SetHeaderLine(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAutoFormatHeaders(true)

	table.SetBorder(false)
	table.SetAutoWrapText(false)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetColumnSeparator("  ")
	table.SetNoWhiteSpace(true)
	table.SetTablePadding("     ")

	table.AppendBulk(data)

	table.Render()
}

func startLoadingBar(text string) *spinner.Spinner {
	s := spinner.New(spinner.CharSets[36], 800*time.Millisecond)
	s.Suffix = "\n" + text
	s.Start()
	return s
}

func destroyDatabase(client *turso.Client, name string) error {
	start := time.Now()
	s := startLoadingBar(fmt.Sprintf("Destroying database %s... ", turso.Emph(name)))
	defer s.Stop()
	if err := client.Databases.Delete(name); err != nil {
		return err
	}
	s.Stop()
	elapsed := time.Since(start)

	fmt.Printf("Destroyed database %s in %d seconds.\n", turso.Emph(name), int(elapsed.Seconds()))
	settings, err := settings.ReadSettings()
	if err == nil {
		settings.InvalidateDbNamesCache()
	}

	settings.DeleteDatabase(name)
	return nil
}

func destroyDatabaseRegion(client *turso.Client, database, region string) error {
	if !isValidRegion(client, region) {
		return fmt.Errorf("location '%s' is not a valid one", region)
	}

	db, err := getDatabase(client, database)
	if err != nil {
		return err
	}

	instances, err := client.Instances.List(db.Name)
	if err != nil {
		return err
	}

	instances = filterInstancesByRegion(instances, region)
	if len(instances) == 0 {
		return fmt.Errorf("could not find any instances of database %s in location %s", db.Name, region)
	}

	primary, replicas := extractPrimary(instances)
	g := errgroup.Group{}
	for i := range replicas {
		replica := replicas[i]
		g.Go(func() error { return deleteDatabaseInstance(client, db.Name, replica.Name) })
	}

	if err := g.Wait(); err != nil {
		return err
	}

	fmt.Printf("Destroyed %d instances in location %s of database %s.\n", len(replicas), turso.Emph(region), turso.Emph(db.Name))
	if primary != nil {
		destroyAllCmd := fmt.Sprintf("turso db destroy %s", database)
		fmt.Printf("Primary was not destroyed. To destroy it, with the whole database, run '%s'\n", destroyAllCmd)
	}

	return nil
}

func destroyDatabaseInstance(client *turso.Client, database, instance string) error {
	err := deleteDatabaseInstance(client, database, instance)
	if err != nil {
		return err
	}
	fmt.Printf("Destroyed instance %s of database %s.\n", turso.Emph(instance), turso.Emph(database))
	return nil
}

func deleteDatabaseInstance(client *turso.Client, database, instance string) error {
	err := client.Instances.Delete(database, instance)
	if err != nil {
		if err.Error() == "could not find database "+database+" to delete instance from" {
			return fmt.Errorf("database %s not found. List known databases using %s", turso.Emph(database), turso.Emph("turso db list"))
		}
		if err.Error() == "could not find instance "+instance+" of database "+database {
			return fmt.Errorf("instance %s not found for database %s. List known instances using %s", turso.Emph(instance), turso.Emph(database), turso.Emph("turso db show "+database))
		}
		return err
	}
	return nil
}

func getTursoUrl() string {
	host := os.Getenv("TURSO_API_BASEURL")
	if host == "" {
		host = "https://api.turso.io"
	}
	return host
}

func promptConfirmation(prompt string) (bool, error) {
	reader := bufio.NewReader(os.Stdin)

	for i := 0; i < 3; i++ {
		fmt.Printf("%s [y/n]: ", prompt)
		input, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}

		input = strings.ToLower(strings.TrimSpace(input))
		if input == "y" || input == "yes" {
			return true, nil
		} else if input == "n" || input == "no" {
			return false, nil
		}

		fmt.Println("Please answer with yes or no.")
	}

	return false, fmt.Errorf("could not get prompt confirmed by user")
}

func dbNameArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	client, err := createTursoClient()
	if err != nil {
		return []string{}, cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 0 {
		return getDatabaseNames(client), cobra.ShellCompDirectiveNoFileComp
	}
	return []string{}, cobra.ShellCompDirectiveNoFileComp
}
