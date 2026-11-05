package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/hyperoptimizer/cli/internal/config"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

var Version = "dev"

type App struct {
	In                                                                  io.Reader
	Out, Err                                                            io.Writer
	Store                                                               CredentialStore
	Getenv                                                              func(string) (string, bool)
	Accessible                                                          bool
	Paused                                                              bool
	OpenBrowser                                                         func(string) error
	Interactive                                                         func() bool
	ConfirmBrowser                                                      func(context.Context) (bool, error)
	SelectOrganization                                                  func(context.Context, []api.Organization) (string, error)
	SelectProject                                                       func(context.Context, []api.Project) (string, error)
	BackOff                                                             func() backoff.BackOff
	Endpoint, ConfigPath, Project, CredentialFile, Output, Organization string
}

func New(in io.Reader, out, err io.Writer) *App {
	a := &App{In: in, Out: out, Err: err, Store: systemCredentialStore{}, Getenv: os.LookupEnv, OpenBrowser: browser.OpenURL, Endpoint: "https://graphql.hyperoptimizer.com", ConfigPath: "hpo.toml", Output: "auto"}
	return a
}
func (a *App) Command() *cobra.Command {
	root := &cobra.Command{Use: "hpo", Short: "Launch and monitor HyperOptimizer experiments", SilenceErrors: true, SilenceUsage: true}
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "hyperoptimizer" {
		root.Use = "hyperoptimizer"
	}
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return failure(2, err.Error()) })
	endpointDefault := a.Endpoint
	flags := root.PersistentFlags()
	flags.StringVar(&a.Endpoint, "api-url", a.Endpoint, "API origin (HTTPS; localhost HTTP supported)")
	flags.StringVar(&a.ConfigPath, "config", a.ConfigPath, "Project TOML file")
	flags.StringVar(&a.Project, "project", "", "Project ID override")
	flags.StringVar(&a.Organization, "org", "", "Organization ID or slug (overrides the saved default)")
	flags.StringVar(&a.CredentialFile, "credential-file", "", "Explicit protected-file credential storage instead of OS keyring")
	flags.BoolVar(&a.Accessible, "accessible", a.Accessible, "Use accessible text prompts")
	flags.StringVar(&a.Output, "output", "auto", "auto, table, plain, or jsonl")
	root.AddCommand(&cobra.Command{Use: "version", Args: cobra.NoArgs, Run: func(_ *cobra.Command, _ []string) { fmt.Fprintf(a.Out, "hpo %s\n", Version) }})
	root.AddCommand(&cobra.Command{Use: "logout", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error { return a.logout() }})
	var stdinKey, noBrowser bool
	login := &cobra.Command{Use: "login", Short: "Connect through browser approval or an API key", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return a.login(cmd.Context(), stdinKey, noBrowser) }}
	login.Flags().BoolVar(&stdinKey, "api-key-stdin", false, "Read API key from standard input")
	login.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the approval URL without opening it")
	root.AddCommand(login)
	auth := &cobra.Command{Use: "auth"}
	auth.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		identity, err := client.Identity(ctx)
		if err != nil {
			return apiError(err, source)
		}
		name := "your account"
		if identity.OrganizationName != nil {
			name = *identity.OrganizationName
		}
		message := "Connected to " + name + " via " + a.Endpoint + " (" + source.label() + ", " + string(identity.Kind) + ")"
		if identity.OwnerUserId != nil {
			message += ". Owner " + *identity.OwnerUserId
		}
		if identity.ExpiresAt != nil {
			message += ". Expires " + *identity.ExpiresAt
		}
		return a.event("authenticated", map[string]any{"organization": identity.OrganizationId, "scopes": identity.Scopes, "apiUrl": a.Endpoint, "credentialSource": source, "credentialKind": identity.Kind, "ownerUserId": identity.OwnerUserId, "expiresAt": identity.ExpiresAt}, message)
	}})
	root.AddCommand(auth)
	root.AddCommand(a.organizationCommand())
	root.AddCommand(a.projectCommand(), a.experimentCommand())
	var createProject string
	initialize := &cobra.Command{Use: "init", Short: "Write a schema-generated project configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return a.init(cmd.Context(), createProject) }}
	initialize.Flags().StringVar(&createProject, "create-project", "", "Create a new project with this name")
	root.AddCommand(initialize)
	var checkFormat bool
	format := &cobra.Command{Use: "fmt", Short: "Format project TOML, preserving comments", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		changed, err := config.FormatFile(a.ConfigPath, checkFormat)
		if err != nil {
			return failure(2, err.Error())
		}
		if checkFormat && changed {
			return failure(2, a.ConfigPath+" needs formatting. Run hpo fmt.")
		}
		return a.event("formatted", map[string]any{"config": a.ConfigPath, "changed": changed}, "Formatted "+a.ConfigPath)
	}}
	format.Flags().BoolVar(&checkFormat, "check", false, "Fail if the file needs formatting; do not write")
	root.AddCommand(format)
	var offline bool
	validate := &cobra.Command{Use: "validate", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		source, parsed, err := config.Read(a.ConfigPath)
		if err != nil {
			return failure(2, err.Error())
		}
		if offline {
			return a.event("validated", map[string]any{"structuralOnly": true}, "Structural validation passed. Semantic, project, plan, and image checks require online validation.")
		}
		client, ctx, keySource, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		project, err := a.project(parsed)
		if err != nil {
			return err
		}
		_, err = a.validate(ctx, client, source, project, keySource, false)
		if err != nil {
			return err
		}
		return a.event("validated", nil, "Configuration valid. Image and launch-time checks run during optimize.")
	}}
	validate.Flags().BoolVar(&offline, "offline", false, "Only check structure against the embedded schema")
	root.AddCommand(validate)
	var name string
	var detach bool
	optimize := &cobra.Command{Use: "optimize", Short: "Validate, build/push, launch, and watch trials", Long: "Validate, build/push, launch, and watch trials.\n\nUnpaid subscription trials run offline; bundle dependencies and datasets in your image. Paid runtime downloads require approved destinations. See https://docs.hyperoptimizer.com/concepts/sandboxing-network-access", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return a.optimize(cmd.Context(), name, detach) }}
	optimize.Flags().StringVar(&name, "name", "", "Experiment name")
	optimize.Flags().BoolVar(&detach, "detach", false, "Return after launch; experiment continues in the cloud")
	optimize.Flags().BoolVar(&a.Paused, "paused", false, "Create a paused experiment without starting execution")
	root.AddCommand(optimize)
	root.AddCommand(&cobra.Command{Use: "watch <experiment-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		_, parsed, err := config.Read(a.ConfigPath)
		if err != nil && a.Project == "" {
			return failure(2, err.Error())
		}
		project, err := a.project(parsed)
		if err != nil {
			return err
		}
		return a.watch(ctx, client, project, args[0], source)
	}})
	root.AddCommand(&cobra.Command{Use: "stop <experiment-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		_, parsed, err := config.Read(a.ConfigPath)
		if err != nil && a.Project == "" {
			return failure(2, err.Error())
		}
		project, err := a.project(parsed)
		if err != nil {
			return err
		}
		err = client.Stop(ctx, project, args[0])
		if err != nil {
			return apiError(err, source)
		}
		return a.event("stop_requested", map[string]any{"experimentId": args[0]}, "Stop requested for "+args[0])
	}})
	completion := &cobra.Command{Use: "completion <bash|zsh|fish|powershell>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return root.GenBashCompletion(a.Out)
		case "zsh":
			return root.GenZshCompletion(a.Out)
		case "fish":
			return root.GenFishCompletion(a.Out, true)
		case "powershell":
			return root.GenPowerShellCompletion(a.Out)
		default:
			return failure(2, "Unsupported shell")
		}
	}}
	root.AddCommand(completion)
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if !cmd.Flags().Changed("api-url") {
			a.Endpoint = endpointDefault
			if value, _ := a.Getenv("HPO_API_URL"); value != "" {
				a.Endpoint = value
			}
		}
		switch a.Output {
		case "auto", "plain", "table", "jsonl":
			return nil
		default:
			return failure(2, "--output must be auto, table, plain, or jsonl")
		}
	}
	return root
}

func (a *App) authorized(ctx context.Context) (*api.Client, context.Context, CredentialSource, error) {
	client, err := newClient(a.Endpoint)
	if err != nil {
		return nil, ctx, "", err
	}
	key, source, err := a.credential()
	if err != nil {
		return nil, ctx, source, err
	}
	organization := a.Organization
	if organization == "" {
		organization, _ = a.Getenv("HPO_ORG")
	}
	if organization == "" && source != credentialEnvironment {
		organization, err = a.defaultOrganization(key)
		if err != nil {
			return nil, ctx, source, err
		}
	}
	return client, api.WithOrganization(authContext(ctx, key), organization), source, nil
}
func (a *App) project(parsed map[string]any) (string, error) {
	if a.Project != "" {
		return a.Project, nil
	}
	if target, ok := parsed["target"].(map[string]any); ok {
		if id, ok := target["projectId"].(string); ok && id != "" {
			return id, nil
		}
	}
	return "", failure(2, "No project selected. Use --project <id> or run hpo init.")
}
func (a *App) validate(ctx context.Context, client *api.Client, source []byte, project string, keySource CredentialSource, checkImage bool) (map[string]any, error) {
	result, err := client.ValidateConfiguration(ctx, project, string(source), checkImage)
	if err != nil {
		return nil, apiError(err, keySource)
	}
	return result, nil
}
func (a *App) event(kind string, data map[string]any, message string) error {
	if a.Output == "jsonl" {
		if data == nil {
			data = map[string]any{}
		}
		data["event"] = kind
		data["version"] = 1
		return json.NewEncoder(a.Out).Encode(data)
	}
	_, err := fmt.Fprintln(a.Out, "[..] "+message)
	return err
}
func (a *App) init(ctx context.Context, createName ...string) error {
	if _, err := os.Stat(a.ConfigPath); err == nil {
		return failure(2, "Configuration already exists: "+a.ConfigPath)
	}
	parsed, err := config.Parse(config.Template())
	if err != nil {
		return err
	}
	project := a.Project
	if len(createName) > 0 && createName[0] != "" {
		if project != "" {
			return failure(2, "Use either --project or --create-project")
		}
		client, auth, source, err := a.authorized(ctx)
		if err != nil {
			return err
		}
		created, err := client.CreateProject(auth, createName[0])
		if err != nil {
			return apiError(err, source)
		}
		project = created.Id
	}
	if project == "" {
		client, auth, source, err := a.authorized(ctx)
		if err != nil {
			return err
		}
		var projects []api.Project
		cursor := ""
		for {
			page, err := client.Projects(auth, cursor, 101)
			if err != nil {
				return apiError(err, source)
			}
			more := len(page) > 100
			if more {
				page = page[:100]
			}
			projects = append(projects, page...)
			if !more {
				break
			}
			next := page[len(page)-1].Id
			if next == "" || next <= cursor {
				return failure(5, "GraphQL returned an invalid project pagination cursor")
			}
			cursor = next
		}
		if len(projects) == 0 {
			return failure(2, "No projects exist. Create one in the dashboard, then run hpo init.")
		}
		if len(projects) == 1 {
			project = projects[0].Id
		} else {
			if !a.interactive() {
				return failure(2, "Multiple projects found; choose --project <id>")
			}
			project, err = a.selectProject(ctx, projects)
			if err != nil {
				return err
			}

		}
	}
	parsed["target"] = map[string]any{"projectId": project}
	encoded, err := config.Encode(parsed)
	if err != nil {
		return err
	}
	if _, err := config.Parse(encoded); err != nil {
		return failure(2, err.Error())
	}
	file, err := os.OpenFile(a.ConfigPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return failure(2, "Cannot create config (file may already exist): "+a.ConfigPath)
	}
	_, err = file.Write(encoded)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return a.event("initialized", map[string]any{"config": a.ConfigPath}, "Created "+a.ConfigPath+". Edit the parameters/objectives and build settings before optimize.")
}
func (a *App) login(ctx context.Context, stdinKey, noBrowser bool) error {
	client, err := newClient(a.Endpoint)
	if err != nil {
		return err
	}
	if stdinKey {
		scanner := bufio.NewScanner(io.LimitReader(a.In, 4096))
		if !scanner.Scan() {
			return failure(3, "No API key provided on stdin")
		}
		key := strings.TrimSpace(scanner.Text())
		_, err := client.Identity(authContext(ctx, key))
		if err != nil {
			return apiError(err, "stdin")
		}
		if err := a.saveCredential(key); err != nil {
			return err
		}
		return a.loginSaved("API key saved.")
	}
	if !noBrowser && !a.interactive() {
		return failure(2, "Noninteractive login requires --api-key-stdin or --no-browser")
	}
	attempt, err := client.StartLogin(ctx)
	if err != nil {
		return apiError(err, "device login")
	}
	loginCtx, cancel := context.WithTimeout(ctx, time.Duration(attempt.ExpiresIn)*time.Second)
	defer cancel()
	loginError := func(err error) error {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return failure(3, "Login expired. Run hpo login again.")
		}
		return err
	}
	fmt.Fprintf(a.Err, "Open %s\nEnter this code in your browser: %s\nThis code is valid for %s. Run hpo login again if it expires.\n", attempt.VerificationUri, attempt.UserCode, (time.Duration(attempt.ExpiresIn) * time.Second).String())
	if !noBrowser {
		open, promptErr := a.confirmBrowser(loginCtx)
		if promptErr != nil {
			return loginError(promptErr)
		}
		if err := loginCtx.Err(); err != nil {
			return loginError(err)
		}
		if open {
			if err := a.OpenBrowser(attempt.VerificationUri); err != nil {
				fmt.Fprintln(a.Err, "Could not open browser. Use the URL above.")
			}
		}
	}
	deadline, _ := loginCtx.Deadline()
	fmt.Fprintf(a.Err, "Code expires at %s. Waiting for approval…\n", deadline.Local().Format("15:04:05"))

	delay := time.Duration(attempt.Interval) * time.Second
	for {
		if err := wait(loginCtx, delay); err != nil {
			return loginError(err)
		}
		var result *api.LoginPoll
		err := a.retry(loginCtx, 0, delay, func() error {
			value, pollErr := client.PollLogin(loginCtx, attempt.DeviceCode)
			if pollErr != nil {
				return apiError(pollErr, "device login")
			}
			result = value
			return nil
		})
		if err != nil {
			return loginError(err)
		}

		switch result.Status {
		case "pending":
			continue
		case "slow_down":
			delay += 5 * time.Second
		case "approved":
			if result.ApiKey == nil {
				return failure(5, "Login response did not include credentials")
			}
			if err := a.saveCredential(*result.ApiKey); err != nil {
				return err
			}
			return a.finishPersonalLogin(ctx, client, *result.ApiKey)
		case "denied":
			return failure(3, "Login declined. No credential was issued.")
		default:
			return failure(3, "Login expired. Run hpo login again.")
		}
	}
}
func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
