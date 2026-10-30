package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// CatalogEntry is one service created with `devforge create`.
type CatalogEntry struct {
	Name         string    `json:"name"`
	Language     string    `json:"language"`
	Database     string    `json:"database"`
	Path         string    `json:"path"`
	AppRepo      string    `json:"appRepo,omitempty"`
	ManifestRepo string    `json:"manifestRepo,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// catalogPath is a JSON file next to where you run the CLI (so it survives `docker run` with a
// volume mount). Override with DEVFORGE_CATALOG.
func catalogPath() string {
	return envOr("DEVFORGE_CATALOG", filepath.Join(".devforge", "catalog.json"))
}

func loadCatalog() ([]CatalogEntry, error) {
	raw, err := os.ReadFile(catalogPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []CatalogEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("catalog %s is corrupt: %w", catalogPath(), err)
	}
	return entries, nil
}

func saveCatalog(entries []CatalogEntry) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(catalogPath()), 0755); err != nil {
		return err
	}
	return os.WriteFile(catalogPath(), raw, 0644)
}

// recordService adds or replaces the entry for a service name.
func recordService(entry CatalogEntry) error {
	entries, err := loadCatalog()
	if err != nil {
		return err
	}
	replaced := false
	for i := range entries {
		if entries[i].Name == entry.Name {
			entries[i] = entry
			replaced = true
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	return saveCatalog(entries)
}

var listJSON bool

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List services created with DevForge",
	Run: func(cmd *cobra.Command, args []string) {
		entries, err := loadCatalog()
		if err != nil {
			fmt.Println("❌ Error:", err)
			os.Exit(1)
		}
		if listJSON {
			raw, _ := json.MarshalIndent(entries, "", "  ")
			if entries == nil {
				raw = []byte("[]")
			}
			fmt.Println(string(raw))
			return
		}
		if len(entries) == 0 {
			fmt.Println("No services yet. Create one with: devforge create --name my-service")
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tLANGUAGE\tDATABASE\tCREATED\tAPP REPO")
		for _, e := range entries {
			repo := e.AppRepo
			if repo == "" {
				repo = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name, e.Language, e.Database, e.CreatedAt.Format("2006-01-02 15:04"), repo)
		}
		w.Flush()
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Output the catalog as JSON")
}
