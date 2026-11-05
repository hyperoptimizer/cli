// Package api connects the CLI to the authored GraphQL contract and Hasura reads.
package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	runtime "github.com/Khan/genqlient/graphql"
	gql "github.com/hyperoptimizer/cli/internal/graphql"
)

type Project = gql.CliProjectsProjects
type ExperimentSummary = gql.CliExperimentSummary
type Identity = gql.CliIdentityCliIdentity
type Organization = gql.CliIdentityCliIdentityOrganizationsCliOrganization
type LoginAttempt = gql.CliStartLoginStartCliLoginCliLoginAttempt
type LoginPoll = gql.CliPollLoginPollCliLoginCliLoginPoll
type Launch = gql.CliLaunchCreateManagedExperimentCreatedExperiment
type LaunchInput = gql.ManagedExperimentCreateInput
type ExperimentIntent = gql.InitialExperimentIntent

const IntentRunning = gql.InitialExperimentIntentRunning
const IntentPaused = gql.InitialExperimentIntentPaused

type RegistryCredential = gql.CliCreateRegistryCredentialsCreateCliRegistryCredentialsCliRegistryCredential

type Experiment struct {
	Id, Name, Status, DesiredStatus string
	MaxTrialCount                   int
	Objectives                      []string
}

type Run struct {
	Id                                       string             `json:"id"`
	Number                                   int                `json:"number"`
	Status                                   string             `json:"status"`
	FinalizationState                        string             `json:"finalizationState"`
	Metrics                                  map[string]float64 `json:"metrics"`
	StartedAt, FinishedAt, ResultsAcceptedAt *time.Time
}

type Client struct{ wire runtime.Client }

func New(endpoint string, httpClient *http.Client, userAgent string) *Client {
	return &Client{wire: runtime.NewClient(endpoint, authenticatedDoer{client: httpClient, userAgent: userAgent})}
}

func (c *Client) Identity(ctx context.Context) (*Identity, error) {
	result, err := gql.CliIdentity(ctx, c.wire)
	if err != nil {
		return nil, clientError(err)
	}
	if result.CliIdentity.KeyId == "" {
		return nil, incompatible()
	}
	return &result.CliIdentity, nil
}
func (c *Client) StartLogin(ctx context.Context) (*LoginAttempt, error) {
	result, err := gql.CliStartLogin(ctx, c.wire)
	if err != nil {
		return nil, clientError(err)
	}
	if result.StartCliLogin.DeviceCode == "" || result.StartCliLogin.VerificationUri == "" || result.StartCliLogin.ExpiresIn <= 0 || result.StartCliLogin.Interval <= 0 {
		return nil, incompatible()
	}
	return &result.StartCliLogin, nil
}
func (c *Client) PollLogin(ctx context.Context, code string) (*LoginPoll, error) {
	result, err := gql.CliPollLogin(ctx, c.wire, gql.CliLoginPollInput{DeviceCode: code})
	if err != nil {
		return nil, clientError(err)
	}
	if result.PollCliLogin.Status == "" {
		return nil, incompatible()
	}
	return &result.PollCliLogin, nil
}
func (c *Client) Projects(ctx context.Context, after string, limit int) ([]Project, error) {
	result, err := gql.CliProjects(ctx, c.wire, after, limit)
	if err != nil {
		return nil, clientError(err)
	}
	if result.Projects == nil {
		return nil, incompatible()
	}
	return result.Projects, nil
}
func (c *Client) Experiments(ctx context.Context, project, after string, limit int) ([]ExperimentSummary, error) {
	rows := make([]ExperimentSummary, 0)
	if project != "" {
		result, err := gql.CliProjectExperiments(ctx, c.wire, project, after, limit)
		if err != nil {
			return nil, clientError(err)
		}
		if result.Experiments == nil {
			return nil, incompatible()
		}
		for _, row := range result.Experiments {
			rows = append(rows, row.CliExperimentSummary)
		}
	} else {
		result, err := gql.CliExperiments(ctx, c.wire, after, limit)
		if err != nil {
			return nil, clientError(err)
		}
		if result.Experiments == nil {
			return nil, incompatible()
		}
		for _, row := range result.Experiments {
			rows = append(rows, row.CliExperimentSummary)
		}
	}
	return rows, nil
}
func (c *Client) CreateProject(ctx context.Context, name string) (*Project, error) {
	result, err := gql.CliCreateProject(ctx, c.wire, gql.CreateProjectInput{Name: name})
	if err != nil {
		return nil, clientError(err)
	}
	if result.CreateProject.Id == "" {
		return nil, incompatible()
	}
	return &Project{Id: result.CreateProject.Id, Name: result.CreateProject.Name}, nil
}
func (c *Client) ValidateConfiguration(ctx context.Context, project, toml string, checkImage bool) (map[string]any, error) {
	result, err := gql.CliValidateConfiguration(ctx, c.wire, gql.CliConfigurationInput{ProjectId: project, Toml: toml, CheckImage: checkImage})
	if err != nil {
		return nil, clientError(err)
	}
	var configuration map[string]any
	if json.Unmarshal(result.ValidateCliConfiguration.Configuration, &configuration) != nil || configuration == nil {
		return nil, incompatible()
	}
	return configuration, nil
}
func (c *Client) RegistryCredential(ctx context.Context) (*RegistryCredential, error) {
	result, err := gql.CliCreateRegistryCredentials(ctx, c.wire)
	if err != nil {
		return nil, clientError(err)
	}
	row := result.CreateCliRegistryCredentials
	if row.RegistryHost == "" || row.ProjectName == "" || row.Username == "" || row.Password == "" {
		return nil, incompatible()
	}
	return &row, nil
}
func (c *Client) Launch(ctx context.Context, input LaunchInput) (*Launch, error) {
	result, err := gql.CliLaunch(ctx, c.wire, input)
	if err != nil {
		return nil, clientError(err)
	}
	if result.CreateManagedExperiment.Id == "" || result.CreateManagedExperiment.DashboardUrl == "" {
		return nil, incompatible()
	}
	return &result.CreateManagedExperiment, nil
}

func (c *Client) Stop(ctx context.Context, project, id string) error {
	result, err := gql.CliStop(ctx, c.wire, gql.ExperimentCommandInput{ProjectId: project, Id: id})
	if err != nil {
		return clientError(err)
	}
	if !result.StopExperiment.Success {
		return incompatible()
	}
	return nil
}
func (c *Client) Experiment(ctx context.Context, project, id string) (*Experiment, error) {
	result, err := gql.CliExperiment(ctx, c.wire, project, id)
	if err != nil {
		return nil, clientError(err)
	}
	if len(result.Experiments) == 0 {
		return nil, &Error{Status: 404, Code: "NOT_FOUND", Message: "Experiment unavailable"}
	}
	row := result.Experiments[0]
	if row.Optimization == nil {
		return nil, incompatible()
	}
	experiment := &Experiment{Id: row.Id, Name: row.Name, Status: row.Optimization.Status, DesiredStatus: row.Optimization.DesiredStatus, MaxTrialCount: row.Optimization.MaxTrialCount}
	for _, metric := range row.Metrics {
		experiment.Objectives = append(experiment.Objectives, metric.Key)
	}
	return experiment, nil
}
func (c *Client) Runs(ctx context.Context, project, id, after string, limit int, metricKeys []string) ([]Run, error) {
	result, err := gql.CliRuns(ctx, c.wire, project, id, after, limit, metricKeys)
	if err != nil {
		return nil, clientError(err)
	}
	if result.Runs == nil {
		return nil, incompatible()
	}
	runs := make([]Run, 0, len(result.Runs))
	for _, row := range result.Runs {
		if row.Optimization == nil {
			return nil, incompatible()
		}
		run := Run{Id: row.Id, Number: row.Optimization.Number, Status: row.Status, FinalizationState: row.FinalizationState, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, ResultsAcceptedAt: row.Optimization.ResultsAcceptedAt, Metrics: map[string]float64{}}
		// Completed finalization reads the immutable store, never a live-value fallback.
		if row.FinalizationState == "complete" {
			for _, metric := range row.FinalMetrics {
				value, err := metricNumber(metric.Value)
				if err != nil {
					return nil, err
				}
				run.Metrics[metric.Key] = value
			}
		} else {
			for _, metric := range row.Metrics {
				value := metric.LastValue
				if metric.Aggregation != nil {
					switch *metric.Aggregation {
					case "min":
						value = metric.MinValue
					case "max":
						value = metric.MaxValue
					case "mean":
						value = metric.MeanValue
					case "explicit":
						value = metric.ExplicitValue
					}
				}
				if value == nil {
					continue
				}
				number, err := metricNumber(*value)
				if err != nil {
					return nil, err
				}
				run.Metrics[metric.Key] = number
			}
		}
		runs = append(runs, run)
	}
	return runs, nil
}
func metricNumber(raw string) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, incompatible()
	}
	return value, nil
}
