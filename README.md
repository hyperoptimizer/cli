# HPO CLI

Open-source Go command-line client for [Hyperoptimizer](https://hyperoptimizer.com), maintained in [`hyperoptimizer/cli`](https://github.com/hyperoptimizer/cli). `hpo` and `hyperoptimizer` run the same program and share credentials/configuration.

## Build and test

From this directory:

```sh
go test -race ./...
go build -o dist/hpo ./cmd/hpo
```

Browser opening uses `pkg/browser`; interactive forms use Charm Huh v0.7, with the existing Bubble Tea major version. ULIDs, retries, atomic writes, and digest validation use OK Log ULID, Cenkalti Backoff v5, Nate Finch Atomic, and OCI go-digest. Application-specific orchestration and TOML presentation rules remain local.

Build with the exact Go version in `go.mod`. Generate the typed API client with
`go generate ./internal/graphql`; the schema snapshot is included here.
See [contract updates](CONTRACTS.md), [release verification](VERIFYING.md), and [security reporting](SECURITY.md).

Install from [GitHub Releases](https://github.com/hyperoptimizer/cli/releases): verify the archive,
extract it, and place `hpo` (or its identical alias `hyperoptimizer`) on your PATH.
For a source build, run `go build -o dist/hpo ./cmd/hpo`.

Telemetry is disabled by default. Setting `BETTER_STACK_CLI_DSN` explicitly opts into
error reporting to that configured Sentry-compatible endpoint. Error reports are
redacted; no monitoring endpoint or credential is embedded in official builds.

## First run

```sh
hpo login
hpo init
# Edit hpo.toml. Set the required workload environment variables.
hpo fmt
hpo validate
hpo optimize
```

Login prints a server-provided URL and code (for example, `H-321-123-132`). Enter that code on the Connect page, continue to review the request and its remaining lifetime, and approve as your signed-in user. Codes expire after ten minutes; run `hpo login` again to get a new one. After approval, the terminal asks which organization to use by default, showing each organization’s plan and subscription status. The `H-` prefix is fixed; you can type the nine digits or paste the full code. The CLI asks **Open in browser?**, defaulting to **No**. Choose Yes to open it once, or No to use the URL manually while the CLI waits for approval. An unavailable browser also falls back to the displayed URL. Use `hpo login --no-browser` on SSH to skip the prompt and browser. For CI set `HPO_API_KEY`, or pipe a key to `hpo login --api-key-stdin`. Credentials are stored in the OS credential store unless `--credential-file <path>` explicitly selects a protected local file on Unix. Windows uses its credential store or `HPO_API_KEY`. Never put credentials in hpo.toml. Credential precedence is explicitly present `HPO_API_KEY`, then an explicitly selected credential file, then the OS credential store. Empty or whitespace-only environment keys fail authentication instead of falling back; rejected keys also never fall back.

Use `hpo org list` to list accessible organizations, plans, and subscription status. `hpo org use <id-or-slug>` saves a default; `hpo org use` opens the selection prompt. Pass `--org <id-or-slug>` to override it for one command. `HPO_ORG` is the environment fallback before the saved default. With no selection, workspace commands require `--org` or `hpo org use`; a removed default never silently falls back to another organization. Noninteractive login saves the login without prompting; select a default afterward or supply `--org`. Defaults are tied to the saved credential, so a different user’s login cannot inherit them. `HPO_API_KEY` does not use the saved login’s default; use `--org` or `HPO_ORG` when that environment credential is personal.

Both login methods report that the credential was saved. If `HPO_API_KEY` remains set, a warning on stderr explains that it overrides the saved credential and tells you to unset it. An empty environment key produces a warning that authenticated commands will fail until you replace or unset it. Warnings do not change successful login/logout exit codes.

Organization discovery reads your current Clerk memberships and initializes missing workspace records automatically; you do not need to visit each workspace in the dashboard first. Removed memberships and deleted workspaces remain unavailable.

Browse the selected workspace with paginated listing commands:

```sh
hpo project list
hpo experiment list
hpo experiment list --project <project-id>
hpo project list --org <other-org-id-or-slug>
hpo experiment list --org <other-org-id-or-slug> --output jsonl
```

Both commands fetch all pages. Experiments include managed optimization and external tracking experiments; `--project` optionally filters the list. `projects list` and `experiments list` are aliases. A one-command `--org` override does not change your saved default.

`hpo auth status` verifies the active credential and displays its source and API URL. JSONL adds `credentialSource` (`environment`, `credential_file`, or `keyring`) and `apiUrl` to the existing `authenticated` event. `hpo logout` clears only the selected local storage backend; it leaves environment variables and server-side keys intact. Logout warns if `HPO_API_KEY` remains set and directs you to dashboard revocation. Its JSONL event is `logged_out`, with `localCredentialRemoved` indicating whether a saved credential existed. No key values are printed.

Interactive project selection supports keyboard navigation and filtering. Use `--accessible` for text prompts; `TERM=dumb` selects text prompts automatically. Prompt and browser-launcher output goes to stderr, including with JSONL output. EOF aborts without accepting a default, Ctrl+C exits 130, and the login code can expire while the prompt is open. Noninteractive login requires `--api-key-stdin` or `--no-browser`.

`hpo init --project <id>` can scaffold without login. Otherwise it selects an existing project; `hpo init --create-project <name>` creates a new one. Init never overwrites a file. Dashboard exports and project documents share one unversioned configuration format. `--config` accepts any filename.

A `[build]` invokes local Docker Buildx and pushes an immutable image to your org registry. `hpo optimize` builds whenever `[build]` is present. To use an existing image, omit `[build]` and supply `[dockerImage]`. Build context/Dockerfile paths are relative to the configuration file. Docker credentials are temporary and kept in an isolated directory.

## Canonical formatting

`hpo fmt` formats the selected `hpo.toml` in place; `hpo fmt --check` exits 2 if formatting is needed. Use `--config path/to/config.toml` for other files. Comments move with their named blocks, fields/sections follow the shared generated policy, and parameter/objective blocks sort by name (parameter ties by slug). Argument, category, and condition order is preserved. Formatting inserts no defaults, and replaces the file atomically only after successful parsing.

Each `[[parameters]]` or `[[objectives]]` adds one named entry. Slugs become trial flags, e.g. `--hpo-learning-rate`; objective names identify metrics, e.g. `hpo.metrics.accuracy`. Multiple objectives retain their own directions and are displayed independently. There is no primary objective or scalar overall winner.

All online commands use one `/graphql` endpoint. Hasura supplies project, experiment and canonical run reads; Yoga/Prisma handles validation, launch, stop, device login and temporary registry credentials. API keys authorize explicit operations and separate Hasura read roles; caller-supplied organization headers never establish identity. CLI-issued keys grant `projects:read/write`, `experiments:read/write`, `runs:read` and `registry:write`. Device codes and API credentials are stored as hashes, and browser approval verifies the user. Terminal credentials are user-owned and expire after 30 days. Each organization request verifies current Clerk membership, so removal blocks that organization without revoking access to others.

## Development runtime

For development against a local API, set `HPO_API_URL=http://127.0.0.1:8186`. Normal use defaults to the hosted public API.

`hpo optimize --paused --detach` creates a paused experiment without requesting execution. The canonical stack includes Hatchet/Kubernetes workers and stdout ingestion. Hosted execution and billing require service access; local CLI tests do not certify hosted service availability. Temporary Harbor push credentials are project-scoped and expire after one day. The Python tracking SDK also uses GraphQL.

Backend integration acceptance runs separately against a pinned CLI revision. This repository’s unit tests require no account, backend secrets, or private source.

## Monitor and recover

```sh
hpo optimize --output plain
hpo optimize --output jsonl --detach
hpo watch <experiment-id> --project <project-id>
hpo stop <experiment-id> --project <project-id>
```

`stop` prevents further trials and lets running trials finish. A gracefully stopped
experiment can therefore finish with status `completed`; `watch` returns exit 0
for that observed outcome, including normal completion at the trial limit.

Auto output chooses a live terminal table or plain lines. JSONL writes structured events to stdout; build diagnostics and errors use stderr. Ctrl+C detaches and leaves cloud execution running. Polling produces current state changes, not a complete historical event log. JSONL run events use `run_updated` and canonical `runId`, with `finalizationState` and `optimizerEligible`. The monitor selects declared objective keys, keeping nested metrics within the product’s objective limit. Finalized runs display frozen objective results; active runs display their retained aggregation snapshots.

A launch timeout saves its ULID/configuration fingerprint in `.hpo/launch.json`; rerun the same command with unchanged configuration and required environment values to recover the original experiment. State contains no environment values or API key. Add `.hpo/` to your project gitignore. Once a launch succeeds, another optimize intentionally creates a new experiment. Never discard pending state without checking the dashboard for the previous launch.

`hpo validate --offline` checks structure only; cross-field and organization/plan/image rules require online validation. Online preflight does not reserve capacity or credits. Launch revalidates.

Temporary transport errors, HTTP 429, and 5xx responses use bounded backoff with jitter and respect `Retry-After`. Launch uses three attempts with the same saved idempotency key; watch uses six attempts for each snapshot. Credential, permission, validation, and incompatible-response errors stop immediately. Device polling obeys the server interval and `slow_down`, and ends when its code expires.

Exit codes: 0 success, 1 failed/cancelled experiment, 2 usage/configuration error, 3 authentication, 4 authorization, 5 service/transport, 130 interruption. A failed trial alone does not fail the experiment.

API URL precedence is explicit `--api-url`, then nonempty `HPO_API_URL`, then `https://graphql.hyperoptimizer.com`. Empty `HPO_API_URL` uses the default. For a local API use `HPO_API_URL=http://127.0.0.1:8186`. The local ingress `HPO_API_URL=http://graphql.127.0.0.1.nip.io` is also supported. HTTP is restricted to localhost, literal loopback addresses, and that exact local ingress hostname; remote API origins require HTTPS. An origin automatically uses `/graphql`; an explicit endpoint ending in `/graphql` is also accepted. Browser verification and experiment dashboard URLs use the new service’s explicit `GRAPHQL_DASHBOARD_URL`. Credentials are scoped to the selected endpoint; log in again when changing origins.

## Releases

`sh package.sh 0.1.0` builds six deterministic archives and a dependency inventory.
Python 3 and the pinned Go toolchain are required. Release tags use `v*`.
The public workflow tests, repeats builds, attests every asset and creates a draft
release for review. See [VERIFYING.md](VERIFYING.md) for details.

For source installs, build the binary and run `sh install.sh dist/hpo "$HOME/.local/bin"`.
The installer refuses to overwrite either command name. `hpo logout` removes local
credentials; dashboard revocation disables the server-side credential.

Unpaid subscription trials run offline. Bundle dependencies and datasets in your image;
paid runtime downloads require approved destinations. See [network access](https://docs.hyperoptimizer.com/concepts/sandboxing-network-access).
