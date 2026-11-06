package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/hyperoptimizer/cli/internal/fileio"
	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
)

type organizationPreference struct {
	Credential   string `json:"credential"`
	Organization string `json:"organization"`
}

func credentialFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
func (a *App) defaultOrganization(key string) (string, error) {
	var raw []byte
	var err error
	if a.CredentialFile != "" {
		raw, err = os.ReadFile(a.CredentialFile + ".org")
	} else {
		var value string
		value, err = a.Store.Get("hyperoptimizer-org", a.Endpoint+"#default-organization")
		raw = []byte(value)
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", failure(3, "Could not read the default organization. Use --org <id>.")
	}
	if len(raw) == 0 {
		return "", nil
	}
	var saved organizationPreference
	if err := json.Unmarshal(raw, &saved); err != nil {
		return "", failure(2, "Invalid default organization. Run hpo org use <id> or pass --org <id>.")
	}
	// A default belongs to this login, never to a replacement/environment credential.
	if saved.Credential != credentialFingerprint(key) {
		return "", nil
	}
	return saved.Organization, nil
}
func (a *App) saveDefaultOrganization(key, organization string) error {
	raw, err := json.Marshal(organizationPreference{credentialFingerprint(key), organization})
	if err != nil {
		return err
	}
	if a.CredentialFile != "" {
		return fileio.WriteProtected(a.CredentialFile+".org", raw)
	}
	if err := a.Store.Set("hyperoptimizer-org", a.Endpoint+"#default-organization", string(raw)); err != nil {
		return failure(3, "Could not save the default organization. Use --org <id>.")
	}
	return nil
}
func findOrganization(organizations []api.Organization, value string) (api.Organization, bool) {
	for _, org := range organizations {
		if org.Id == value || (org.Slug != nil && *org.Slug == value) {
			return org, true
		}
	}
	return api.Organization{}, false
}
func (a *App) selectOrganization(ctx context.Context, organizations []api.Organization) (string, error) {
	if a.SelectOrganization != nil {
		return a.SelectOrganization(ctx, organizations)
	}
	options := make([]huh.Option[string], 0, len(organizations))
	for _, org := range organizations {
		options = append(options, huh.NewOption(fmt.Sprintf("%s · %s (%s) · %s", org.Name, org.Plan, org.SubscriptionStatus, org.Id), org.Id))
	}
	var selected string
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Which organization should this terminal use by default?").Options(options...).Filtering(true).Value(&selected)))
	if err := a.runPrompt(ctx, form); err != nil {
		return "", err
	}
	return selected, nil
}
func (a *App) printOrganizations(organizations []api.Organization, selected string) error {
	rows := [][]string{}
	for _, org := range organizations {
		marker := ""
		if org.Id == selected && a.Output != "csv" {
			marker = " *"
		}
		rows = append(rows, []string{org.Name + marker, org.Plan, org.SubscriptionStatus, org.Id})
	}
	return a.listOutput("organizations", map[string]any{"organizations": organizations, "defaultOrganization": selected}, []string{"ORGANIZATION", "PLAN", "SUBSCRIPTION", "ID"}, rows, "You do not currently belong to an available organization.")
}
func (a *App) finishPersonalLogin(ctx context.Context, client *api.Client, key string) error {
	// Credential is already saved; workspace selection does not determine authentication.
	identity, err := client.Identity(authContext(ctx, key))
	if err != nil {
		return failure(3, "Login saved, but organizations could not be loaded. Run hpo org list to retry.")
	}
	selected := a.Organization
	if selected == "" {
		selected, _ = a.Getenv("HPO_ORG")
	}
	if selected == "" && len(identity.Organizations) > 0 && a.interactive() {
		selected, err = a.selectOrganization(ctx, identity.Organizations)
		if err != nil {
			fmt.Fprintln(a.Err, "Login saved. Choose a default later with hpo org use <id>.")
			return err
		}
	}
	if selected != "" {
		org, ok := findOrganization(identity.Organizations, selected)
		if !ok {
			return failure(2, "Login saved, but that organization is unavailable. Run hpo org list.")
		}
		if err := a.saveDefaultOrganization(key, org.Id); err != nil {
			return err
		}
		fmt.Fprintf(a.Err, "Default organization: %s · %s (%s). Override with --org <id>.\n", org.Name, org.Plan, org.SubscriptionStatus)
	} else {
		fmt.Fprintln(a.Err, "Login saved without a default organization. Run hpo org list, then hpo org use <id>; or pass --org <id>.")
	}
	return a.loginSaved("Personal terminal login saved.")
}
func (a *App) organizationCommand() *cobra.Command {
	root := &cobra.Command{Use: "org", Short: "List organizations and choose the default for this terminal"}
	load := func(ctx context.Context) (*api.Identity, string, error) {
		key, source, err := a.credential()
		if err != nil {
			return nil, "", err
		}
		client, err := newClient(a.Endpoint)
		if err != nil {
			return nil, "", err
		}
		identity, err := client.Identity(authContext(ctx, key))
		if err != nil {
			return nil, "", apiError(err, source)
		}
		return identity, key, nil
	}
	root.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		identity, key, err := load(cmd.Context())
		if err != nil {
			return err
		}
		selected, err := a.defaultOrganization(key)
		if err != nil {
			return err
		}
		return a.printOrganizations(identity.Organizations, selected)
	}})
	root.AddCommand(&cobra.Command{Use: "use [id-or-slug]", Short: "Save the default organization for this login", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		identity, key, err := load(cmd.Context())
		if err != nil {
			return err
		}
		var selected string
		if len(args) > 0 {
			selected = args[0]
		} else {
			if len(identity.Organizations) == 0 {
				return failure(2, "No organizations available. Join or create one in the dashboard.")
			}
			if !a.interactive() {
				return failure(2, "Specify an organization: hpo org use <id>")
			}
			selected, err = a.selectOrganization(cmd.Context(), identity.Organizations)
			if err != nil {
				return err
			}
		}
		org, ok := findOrganization(identity.Organizations, selected)
		if !ok {
			return failure(2, "Organization unavailable. Run hpo org list.")
		}
		if err := a.saveDefaultOrganization(key, org.Id); err != nil {
			return err
		}
		return a.event("organization_selected", map[string]any{"organization": org.Id, "plan": org.Plan}, "Default organization: "+org.Name+" · "+org.Plan)
	}})
	return root
}
