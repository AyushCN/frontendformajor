package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// sensitiveKeyRegex matches env keys that usually contain credentials or secrets.
	// Includes URL/DSN/CONN/DATABASE because connection strings embed passwords by design.
	sensitiveKeyRegex = regexp.MustCompile(`(?i)(KEY|SECRET|TOKEN|PASSWORD|AUTH|PRIVATE|CREDENTIAL|CERT|PASSWD|DATABASE_URL|DATABASE|DSN|CONN_STR|CONNECTION)`)

	// credentialURLRegex matches URLs containing username:password@
	credentialURLRegex = regexp.MustCompile(`([a-zA-Z0-9+.-]+://)([^:/@\s]+):([^/@\s]+)@`)

	// bearerTokenRegex matches Bearer tokens in headers or values
	bearerTokenRegex = regexp.MustCompile(`(?i)(bearer\s+)([a-zA-Z0-9_\-\.]{16,})`)

	// cliSecretFlagRegex matches --password=..., --token=..., --key=...
	cliSecretFlagRegex = regexp.MustCompile(`(?i)(--(?:key|token|password|secret|auth)[= ])([^\s]+)`)

	// jwtRegex matches JSON Web Tokens
	jwtRegex = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)
)

// ResolvedBuildPlan is the validated output of the analysis, runtime resolution, and builder selection pipeline.
type ResolvedBuildPlan struct {
	ID                      string                   `json:"id"`
	SandboxID               string                   `json:"sandbox_id"`
	SelectedBuilder         string                   `json:"selected_builder"`        // "railpack", "dockerfile", "nixpacks"
	BuilderSelectionSource  string                   `json:"builder_selection_source"`// "USER_EXPLICIT", "AUTOMATIC_DOCKERFILE", "AUTOMATIC_RAILPACK"
	DockerfilePath          string                   `json:"dockerfile_path,omitempty"`
	RootDir                 string                   `json:"root_dir"`
	RelativeRootDir         string                   `json:"relative_root_dir"`
	PrimaryLanguage         string                   `json:"primary_language"`
	Category                ProjectCategory          `json:"category"`
	Runtime                 RuntimeResolution        `json:"runtime"`
	Recommendation          DockerfileRecommendation `json:"recommendation"`
	InternalPort            int                      `json:"internal_port"`
	StartCommand            string                   `json:"start_command"`
	BuildCommand            string                   `json:"build_command,omitempty"`
	InstallCommand          string                   `json:"install_command,omitempty"`
	PreserveImageEntrypoint bool                     `json:"preserve_image_entrypoint"`
	ExecutionMode           ExecutionMode            `json:"execution_mode"`
	EntryPoint              *EntryPointResolution    `json:"entry_point,omitempty"`
	EnvironmentVariables    map[string]string        `json:"environment_variables,omitempty"`
	CreatedAt               time.Time                `json:"created_at"`
}

// Clone returns a true deep copy of the ResolvedBuildPlan ensuring structural immutability.
func (p *ResolvedBuildPlan) Clone() *ResolvedBuildPlan {
	if p == nil {
		return nil
	}
	cp := *p

	// Deep copy Runtime slices
	if p.Runtime.Constraints != nil {
		cp.Runtime.Constraints = make([]string, len(p.Runtime.Constraints))
		copy(cp.Runtime.Constraints, p.Runtime.Constraints)
	}
	if p.Runtime.Warnings != nil {
		cp.Runtime.Warnings = make([]string, len(p.Runtime.Warnings))
		copy(cp.Runtime.Warnings, p.Runtime.Warnings)
	}
	if p.Runtime.Conflicts != nil {
		cp.Runtime.Conflicts = make([]string, len(p.Runtime.Conflicts))
		copy(cp.Runtime.Conflicts, p.Runtime.Conflicts)
	}

	// Deep copy Recommendation slices
	if p.Recommendation.RequiredSysLibs != nil {
		cp.Recommendation.RequiredSysLibs = make([]string, len(p.Recommendation.RequiredSysLibs))
		copy(cp.Recommendation.RequiredSysLibs, p.Recommendation.RequiredSysLibs)
	}
	if p.Recommendation.TriggerPackages != nil {
		cp.Recommendation.TriggerPackages = make([]string, len(p.Recommendation.TriggerPackages))
		copy(cp.Recommendation.TriggerPackages, p.Recommendation.TriggerPackages)
	}

	// Deep copy EntryPoint
	if p.EntryPoint != nil {
		epCopy := *p.EntryPoint
		if p.EntryPoint.PrimaryCandidate != nil {
			pcCopy := *p.EntryPoint.PrimaryCandidate
			epCopy.PrimaryCandidate = &pcCopy
		}
		if p.EntryPoint.Candidates != nil {
			epCopy.Candidates = make([]EntryPointCandidate, len(p.EntryPoint.Candidates))
			copy(epCopy.Candidates, p.EntryPoint.Candidates)
		}
		if p.EntryPoint.Warnings != nil {
			epCopy.Warnings = make([]string, len(p.EntryPoint.Warnings))
			copy(epCopy.Warnings, p.EntryPoint.Warnings)
		}
		cp.EntryPoint = &epCopy
	}

	// Deep copy EnvironmentVariables
	if p.EnvironmentVariables != nil {
		cp.EnvironmentVariables = make(map[string]string, len(p.EnvironmentVariables))
		for k, v := range p.EnvironmentVariables {
			cp.EnvironmentVariables[k] = v
		}
	}

	return &cp
}

// RedactedCopy returns a deep copy of the plan where all sensitive environment variables,
// connection strings, and credential tokens are masked.
// Safe for transmission over API or inclusion in diagnostic error responses.
func (p *ResolvedBuildPlan) RedactedCopy() ResolvedBuildPlan {
	cp := p.Clone()
	if cp == nil {
		return ResolvedBuildPlan{}
	}

	if cp.EnvironmentVariables != nil {
		redactedEnv := make(map[string]string, len(cp.EnvironmentVariables))
		for k, v := range cp.EnvironmentVariables {
			redactedEnv[k] = RedactValue(k, v)
		}
		cp.EnvironmentVariables = redactedEnv
	}

	// Also redact any potential secrets embedded in StartCommand or BuildCommand
	cp.StartCommand = RedactString(cp.StartCommand)
	cp.BuildCommand = RedactString(cp.BuildCommand)

	return *cp
}

// RedactValue evaluates both the key and the value to mask sensitive information.
func RedactValue(key, value string) string {
	if sensitiveKeyRegex.MatchString(key) {
		return "[REDACTED]"
	}
	return RedactString(value)
}

// RedactString masks embedded credentials in connection strings, Bearer tokens, or high-entropy secrets.
func RedactString(s string) string {
	if s == "" {
		return ""
	}
	// Mask URLs: scheme://user:pass@host -> scheme://user:[REDACTED]@host
	res := credentialURLRegex.ReplaceAllString(s, "${1}${2}:[REDACTED]@")

	// Mask CLI secret flags: --token=xyz -> --token=[REDACTED]
	res = cliSecretFlagRegex.ReplaceAllString(res, "${1}[REDACTED]")

	// Mask JWT tokens
	res = jwtRegex.ReplaceAllString(res, "[REDACTED_JWT]")

	// Mask Bearer tokens
	res = bearerTokenRegex.ReplaceAllString(res, "${1}[REDACTED]")

	// High entropy standalone tokens (>32 non-whitespace characters)
	if len(res) >= 32 && !strings.Contains(res, " ") && !strings.Contains(res, "/") {
		return "[REDACTED]"
	}
	return res
}

// BuildPlanner coordinates repository analysis, runtime resolution, and strategy selection.
type BuildPlanner struct {
	analyzer           *RepositoryAnalyzer
	runtimeResolver    *RuntimeResolver
	strategySelector   *BuilderStrategySelector
	entryPointResolver *EntryPointResolver
}

// NewBuildPlanner constructs a new BuildPlanner.
func NewBuildPlanner() *BuildPlanner {
	return &BuildPlanner{
		analyzer:           NewRepositoryAnalyzer(),
		runtimeResolver:    NewRuntimeResolver(),
		strategySelector:   NewBuilderStrategySelector(),
		entryPointResolver: NewEntryPointResolver(),
	}
}

// PlanBuild executes the complete Phase 1 + Phase 2 intelligence pipeline.
// Invariant: The original repository is inspected purely in read-only mode and is NEVER modified.
func (p *BuildPlanner) PlanBuild(ctx context.Context, sandboxID, workDir string, userCfg *UserBuildConfig) (*ResolvedBuildPlan, error) {
	if workDir == "" {
		return nil, fmt.Errorf("build planner: workDir cannot be empty")
	}

	relativeRootDir := "."
	effectiveDir := workDir
	if userCfg != nil && strings.TrimSpace(userCfg.RootDir) != "" {
		clean := filepath.Clean(strings.TrimSpace(userCfg.RootDir))
		if strings.HasPrefix(clean, "..") {
			return nil, fmt.Errorf("build planner: invalid root_dir %q traverses outside repository", userCfg.RootDir)
		}
		relativeRootDir = clean
		effectiveDir = filepath.Join(workDir, clean)
	}

	// Step 1: Repository Analysis (Phase 1)
	analysis, err := p.analyzer.InspectRepository(ctx, effectiveDir)
	if err != nil {
		return nil, fmt.Errorf("build planner: repository analysis failed: %w", err)
	}

	// Step 2: Runtime Compatibility Resolution (Phase 2 Component A)
	runtimeRes, err := p.runtimeResolver.ResolveRuntime(effectiveDir, analysis, userCfg)
	if err != nil {
		return nil, fmt.Errorf("build planner: runtime resolution failed: %w", err)
	}

	// Step 3: Builder Strategy Selection (Phase 2 Component B)
	builderSel, err := p.strategySelector.SelectBuilder(effectiveDir, analysis, userCfg)
	if err != nil {
		return nil, fmt.Errorf("build planner: builder selection failed: %w", err)
	}

	// Step 4: Dockerfile Recommendation (Phase 2 Component B)
	recommendation := p.strategySelector.EvaluateDockerfileRecommendation(analysis, builderSel.SelectedBuilder)

	// Step 4.5: Entry Point Resolution & Execution Mode Detection (Phase 2.5)
	entryPointRes, err := p.entryPointResolver.ResolveEntryPoints(ctx, effectiveDir, analysis, userCfg)
	if err != nil {
		return nil, fmt.Errorf("build planner: entry point resolution failed: %w", err)
	}

	// Step 5: Command and Port Resolution
	preserveEntrypoint := true
	startCmd := ""
	buildCmd := ""
	installCmd := ""
	port := 8080

	// Check project configuration for start command if not a Dockerfile
	if builderSel.SelectedBuilder != "dockerfile" {
		if rjStart := readRailpackJSONStart(effectiveDir); rjStart != "" {
			startCmd = rjStart
		} else if procStart := readProcfileWeb(effectiveDir); procStart != "" {
			startCmd = procStart
		} else if entryPointRes != nil && entryPointRes.SelectedCommand != "" {
			startCmd = entryPointRes.SelectedCommand
			preserveEntrypoint = false
		}
	}

	// Explicit user command overrides
	if userCfg != nil {
		if strings.TrimSpace(userCfg.StartCmd) != "" {
			startCmd = strings.TrimSpace(userCfg.StartCmd)
			preserveEntrypoint = false
		}
		if strings.TrimSpace(userCfg.BuildCmd) != "" {
			buildCmd = strings.TrimSpace(userCfg.BuildCmd)
		}
		if strings.TrimSpace(userCfg.InstallCmd) != "" {
			installCmd = strings.TrimSpace(userCfg.InstallCmd)
		}
		if userCfg.Port > 0 {
			port = userCfg.Port
		}
	}

	// Env map copy
	envVars := make(map[string]string)
	if userCfg != nil && userCfg.Env != nil {
		for k, v := range userCfg.Env {
			envVars[k] = v
		}
	}

	execMode := ExecutionModeApplication
	if entryPointRes != nil && entryPointRes.ExecutionMode != "" {
		execMode = entryPointRes.ExecutionMode
	}

	plan := &ResolvedBuildPlan{
		ID:                      fmt.Sprintf("plan-%d", time.Now().UnixNano()),
		SandboxID:               sandboxID,
		SelectedBuilder:         builderSel.SelectedBuilder,
		BuilderSelectionSource:  builderSel.SelectionSource,
		DockerfilePath:          builderSel.DockerfilePath,
		RootDir:                 effectiveDir,
		RelativeRootDir:         relativeRootDir,
		PrimaryLanguage:         analysis.PrimaryLanguage,
		Category:                analysis.Category,
		Runtime:                 runtimeRes,
		Recommendation:          recommendation,
		InternalPort:            port,
		StartCommand:            startCmd,
		BuildCommand:            buildCmd,
		InstallCommand:          installCmd,
		PreserveImageEntrypoint: preserveEntrypoint,
		ExecutionMode:           execMode,
		EntryPoint:              entryPointRes,
		EnvironmentVariables:    envVars,
		CreatedAt:               time.Now().UTC(),
	}

	return plan, nil
}
