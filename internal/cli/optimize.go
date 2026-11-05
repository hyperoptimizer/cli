package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/hyperoptimizer/cli/internal/config"
	"github.com/hyperoptimizer/cli/internal/fileio"
	"github.com/oklog/ulid/v2"
	"github.com/opencontainers/go-digest"
)

type launchState struct {
	RequestKey   string         `json:"requestKey"`
	SourceHash   string         `json:"sourceHash"`
	PayloadHash  string         `json:"payloadHash"`
	Config       map[string]any `json:"config"`
	Name         string         `json:"name"`
	ExperimentID string         `json:"experimentId,omitempty"`
}

func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func saveState(path string, state launchState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return fileio.WriteProtected(path, data)
}

func (a *App) optimize(ctx context.Context, name string, detach bool) error {
	source, parsed, err := config.Read(a.ConfigPath)
	if err != nil {
		return failure(2, err.Error())
	}
	project, err := a.project(parsed)
	if err != nil {
		return err
	}
	client, ctx, keySource, err := a.authorized(ctx)
	if err != nil {
		return err
	}
	normalized, err := a.validate(ctx, client, source, project, keySource, false)
	if err != nil {
		return err
	}
	sourceHash := hashBytes(append(source, []byte("\n"+a.Endpoint+"\n"+project)...))
	statePath := filepath.Join(filepath.Dir(a.ConfigPath), ".hpo", "launch.json")
	state := launchState{SourceHash: sourceHash, Name: name}
	var previous launchState
	if data, readErr := os.ReadFile(statePath); readErr == nil {
		if err = json.Unmarshal(data, &previous); err != nil {
			return failure(2, "Cannot read pending launch state; move .hpo/launch.json aside after checking the dashboard")
		}
		if previous.ExperimentID == "" {
			if previous.SourceHash != sourceHash || (name != "" && name != previous.Name) {
				return failure(2, "A previous launch may still be pending. Restore its config/name to retry, or check the dashboard before moving .hpo/launch.json aside.")
			}
			state = previous
			normalized = previous.Config
			fmt.Fprintln(a.Err, "Recovering previous launch request…")
		}
	}
	if state.RequestKey == "" {
		state.RequestKey = ulid.Make().String()
	}
	if state.Name == "" {
		state.Name = "training-" + time.Now().UTC().Format("20060102-150405")
	}
	// Resolve only explicitly required workload environment values.
	envs := []map[string]string{}
	if names, ok := normalized["requiredEnv"].([]any); ok {
		for _, raw := range names {
			name, ok := raw.(string)
			if !ok {
				return failure(2, "Invalid requiredEnv")
			}
			value, present := a.Getenv(name)
			if !present || value == "" {
				return failure(2, "Missing required environment variable: "+name)
			}
			envs = append(envs, map[string]string{"name": name, "value": value})
		}
	}
	if state.Config == nil {
		if _, build := normalized["build"]; build {
			if err = a.build(ctx, client, normalized, keySource, state.RequestKey); err != nil {
				return err
			}
		}
		state.Config = normalized
	}
	normalizedTOML, err := config.Encode(normalized)
	if err != nil {
		return err
	}
	normalized, err = a.validate(ctx, client, normalizedTOML, project, keySource, true)
	if err != nil {
		return err
	}
	payload := map[string]any{}
	for key, value := range normalized {
		switch key {
		case "requiredEnv", "build", "target":
		default:
			payload[key] = value
		}
	}
	metadata := map[string]any{"target": map[string]any{"projectId": project}}
	if build, ok := normalized["build"]; ok {
		metadata["build"] = build
	}
	payload["projectConfigMetadata"] = metadata
	payload["name"] = state.Name
	intent := api.IntentRunning
	if a.Paused {
		intent = api.IntentPaused
	}
	payload["desiredStatus"] = string(intent)
	payload["envVars"] = envs
	bytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	bodyHash := hashBytes(bytes)
	if state.PayloadHash != "" && state.PayloadHash != bodyHash {
		return failure(2, "Pending launch inputs changed (including required environment values). Restore the original inputs to recover the launch.")
	}
	state.PayloadHash = bodyHash
	if err = saveState(statePath, state); err != nil {
		return err
	}
	request := api.LaunchInput{ProjectId: project, RequestKey: state.RequestKey, Name: state.Name, DesiredStatus: &intent, Configuration: bytes}
	if description, ok := payload["description"].(string); ok {
		request.Description = &description
	}
	var experiment *api.Launch
	err = a.retry(ctx, 3, 0, func() error {
		result, launchErr := client.Launch(ctx, request)
		if launchErr != nil {
			return apiError(launchErr, keySource)
		}
		experiment = result
		return nil
	})
	if err != nil {
		var exit *ExitError
		if errors.As(err, &exit) && exit.Retryable {
			return failure(5, "Launch outcome is uncertain. Run the same hpo optimize command to recover it; request state is saved in .hpo/launch.json.")
		}
		return err
	}

	state.ExperimentID = experiment.Id
	if err = saveState(statePath, state); err != nil {
		return err
	}
	url := experiment.DashboardUrl
	message := fmt.Sprintf("Experiment %q created (%s)", experiment.Name, experiment.Id)
	if url != "" {
		message += "\n" + url
	}
	if err = a.event("experiment_created", map[string]any{"experimentId": experiment.Id, "name": experiment.Name, "url": url}, message); err != nil {
		return err
	}
	if detach || a.Paused {
		return nil
	}
	return a.watch(ctx, client, project, experiment.Id, keySource)
}

func (a *App) build(ctx context.Context, client *api.Client, document map[string]any, source CredentialSource, requestKey string) error {
	build, ok := document["build"].(map[string]any)
	if !ok {
		return failure(2, "Invalid build settings")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return failure(2, "Docker is required for [build]. Install Docker with Buildx or use an existing dockerImage.")
	}
	run := func(args []string, input io.Reader, output io.Writer) error {
		cmd := exec.CommandContext(ctx, docker, args...)
		cmd.Stdin = input
		cmd.Stdout = output
		cmd.Stderr = a.Err
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return failure(2, "Docker command failed; inspect the build/push output above.")
		}
		return nil
	}
	if err = run([]string{"buildx", "version"}, nil, io.Discard); err != nil {
		return err
	}
	registry, err := client.RegistryCredential(ctx)
	if err != nil {
		return apiError(err, source)
	}
	directory, err := os.MkdirTemp("", "hpo-docker-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	prefix, err := a.dockerContext(ctx, docker, directory)
	if err != nil {
		return err
	}
	if err = run(append(append([]string{}, prefix...), "login", registry.RegistryHost, "--username", registry.Username, "--password-stdin"), strings.NewReader(registry.Password), a.Err); err != nil {
		return err
	}
	repository, _ := build["repository"].(string)
	platform, _ := build["platform"].(string)
	contextPath, _ := build["context"].(string)
	dockerfile, _ := build["dockerfile"].(string)
	base, err := filepath.Abs(filepath.Dir(a.ConfigPath))
	if err != nil {
		return err
	}
	if !filepath.IsAbs(contextPath) {
		contextPath = filepath.Join(base, contextPath)
	}
	if !filepath.IsAbs(dockerfile) {
		dockerfile = filepath.Join(base, dockerfile)
	}
	tag := "hpo-" + requestKey
	image := registry.RegistryHost + "/" + registry.ProjectName + "/" + repository + ":" + tag
	metadata := filepath.Join(directory, "metadata.json")
	if err = a.event("build_started", map[string]any{"image": image}, "Building and pushing "+image); err != nil {
		return err
	}
	if err = run(append(append([]string{}, prefix...), "buildx", "build", "--platform", platform, "--file", dockerfile, "--tag", image, "--push", "--metadata-file", metadata, "--progress", "plain", contextPath), nil, a.Err); err != nil {
		return err
	}
	data, err := os.ReadFile(metadata)
	if err != nil {
		return err
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		return err
	}
	rawDigest, ok := result["containerimage.digest"].(string)
	parsedDigest, digestErr := digest.Parse(rawDigest)
	if !ok || digestErr != nil || parsedDigest.Algorithm() != digest.SHA256 {
		return failure(2, "Buildx did not return an immutable image digest")
	}
	document["dockerImage"] = map[string]any{"registry": registry.RegistryHost, "image": registry.ProjectName + "/" + repository, "tag": tag, "digest": parsedDigest.String()}
	return a.event("image_pushed", map[string]any{"image": image, "digest": parsedDigest.String()}, "Image pushed: "+parsedDigest.String())
}

// Keep the selected daemon and plugin discovery while isolating registry credentials.
func (a *App) dockerContext(ctx context.Context, docker, directory string) ([]string, error) {
	original, _ := a.Getenv("DOCKER_CONFIG")
	if original == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		original = filepath.Join(home, ".docker")
	}
	plugins := struct {
		CliPluginsExtraDirs []string `json:"cliPluginsExtraDirs"`
	}{}
	if data, err := os.ReadFile(filepath.Join(original, "config.json")); err == nil {
		_ = json.Unmarshal(data, &plugins)
	}
	plugins.CliPluginsExtraDirs = append(plugins.CliPluginsExtraDirs, filepath.Join(original, "cli-plugins"))
	data, err := json.Marshal(plugins)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(directory, "config.json"), data, 0600); err != nil {
		return nil, err
	}
	prefix := []string{"--config", directory}
	selected, err := exec.CommandContext(ctx, docker, "context", "show").Output()
	if err != nil {
		return nil, failure(2, "Could not resolve the current Docker context")
	}
	name := strings.TrimSpace(string(selected))
	if name == "default" || name == "" {
		return prefix, nil
	}
	exported, err := exec.CommandContext(ctx, docker, "context", "export", name, "-").Output()
	if err != nil {
		return nil, failure(2, "Could not export the current Docker context")
	}
	imported := exec.CommandContext(ctx, docker, "--config", directory, "context", "import", "hpo-build", "-")
	imported.Stdin = strings.NewReader(string(exported))
	imported.Stdout = io.Discard
	imported.Stderr = a.Err
	if err = imported.Run(); err != nil {
		return nil, failure(2, "Could not prepare an isolated Docker context")
	}
	return append(prefix, "--context", "hpo-build"), nil
}
