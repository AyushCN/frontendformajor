package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestLegacyRuntime_Python35Handling(t *testing.T) {
	policy := NewLegacyRuntimePolicy()

	// 1. Evaluate Python 3.5
	status, strategy, approvedImage := policy.EvaluateVersion("python", "3.5")
	if status != VersionProvisioningRequired {
		t.Errorf("expected VersionProvisioningRequired for Python 3.5, got %s", status)
	}
	if strategy != "dockerfile" {
		t.Errorf("expected isolated legacy dockerfile strategy, got %s", strategy)
	}
	if approvedImage != "python:3.5-slim" {
		t.Errorf("expected approved image python:3.5-slim, got %s", approvedImage)
	}

	// 2. Resolve via RuntimeResolver
	resolver := NewRuntimeResolverWithPolicy(policy)
	analysis := &RepositoryAnalysis{
		PrimaryLanguage: "python",
		Category:        CategoryStandardPython,
	}
	userCfg := &UserBuildConfig{
		RuntimeVersion: "3.5",
	}

	res, err := resolver.ResolveRuntime("/tmp/fake", analysis, userCfg)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	// Invariant: MUST NOT silently upgrade to 3.11 or 3.12!
	if res.Version != "3.5" {
		t.Errorf("CRITICAL VIOLATION: silently changed version! Got %s, want 3.5", res.Version)
	}
	if res.ExactVersion != "3.5.10" {
		t.Errorf("expected exact version 3.5.10, got %s", res.ExactVersion)
	}
	if res.Status != VersionProvisioningRequired {
		t.Errorf("expected VersionProvisioningRequired, got %s", res.Status)
	}
	if res.Strategy != "dockerfile" {
		t.Errorf("expected dockerfile strategy, got %s", res.Strategy)
	}
	if res.LegacyImage != "python:3.5-slim" {
		t.Errorf("expected legacy image python:3.5-slim, got %s", res.LegacyImage)
	}

	// Warning must explain Railpack unsupported path and routing to legacy strategy
	var warningFound bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "not supported by standard Railpack provider") && strings.Contains(w, "python:3.5-slim") {
			warningFound = true
			break
		}
	}
	if !warningFound {
		t.Errorf("expected actionable warning about Railpack unsupported path; got: %+v", res.Warnings)
	}

	// 3. Strategy Selector must route to isolated legacy Dockerfile strategy
	selector := NewBuilderStrategySelector()
	builderSel, err := selector.SelectBuilderWithRuntime("/tmp/fake", analysis, userCfg, &res)
	if err != nil {
		t.Fatalf("builder selection failed: %v", err)
	}
	if builderSel.SelectedBuilder != "dockerfile" || builderSel.SelectionSource != "LEGACY_RUNTIME_STRATEGY" {
		t.Errorf("expected dockerfile with LEGACY_RUNTIME_STRATEGY, got builder=%s source=%s",
			builderSel.SelectedBuilder, builderSel.SelectionSource)
	}
}

func TestLegacyRuntime_UnsupportedVersionsProduceActionableErrors(t *testing.T) {
	policy := NewLegacyRuntimePolicy()
	resolver := NewRuntimeResolverWithPolicy(policy)

	// Case 1: Python 3.4 (obsolete with no approved image)
	res34, err := resolver.ResolveRuntime("/tmp/fake", &RepositoryAnalysis{PrimaryLanguage: "python"}, &UserBuildConfig{RuntimeVersion: "3.4"})
	if err != nil {
		t.Fatalf("unexpected resolver error: %v", err)
	}
	if res34.Status != VersionUnsupported {
		t.Errorf("expected VersionUnsupported for Python 3.4, got %s", res34.Status)
	}
	if len(res34.Conflicts) == 0 || !strings.Contains(res34.Conflicts[0], "obsolete legacy version") {
		t.Errorf("expected actionable conflict error for Python 3.4; got: %+v", res34.Conflicts)
	}

	// Case 2: Python 2.7 (EOL security policy violation)
	res27, _ := resolver.ResolveRuntime("/tmp/fake", &RepositoryAnalysis{PrimaryLanguage: "python"}, &UserBuildConfig{RuntimeVersion: "2.7"})
	if res27.Status != VersionUnsupported {
		t.Errorf("expected VersionUnsupported for Python 2.7, got %s", res27.Status)
	}
	if len(res27.Conflicts) == 0 || !strings.Contains(res27.Conflicts[0], "Security policy prohibits running Python 2.x") {
		t.Errorf("expected security policy error for Python 2.7; got: %+v", res27.Conflicts)
	}

	// Case 3: Node.js 10
	resNode10, _ := resolver.ResolveRuntime("/tmp/fake", &RepositoryAnalysis{PrimaryLanguage: "nodejs"}, &UserBuildConfig{RuntimeVersion: "10.0.0"})
	if resNode10.Status != VersionUnsupported {
		t.Errorf("expected VersionUnsupported for Node 10, got %s", resNode10.Status)
	}

	// Case 4: Go 1.15
	resGo15, _ := resolver.ResolveRuntime("/tmp/fake", &RepositoryAnalysis{PrimaryLanguage: "golang"}, &UserBuildConfig{RuntimeVersion: "1.15.0"})
	if resGo15.Status != VersionUnsupported {
		t.Errorf("expected VersionUnsupported for Go 1.15, got %s", resGo15.Status)
	}

	// Case 5: Java 8
	resJava8, _ := resolver.ResolveRuntime("/tmp/fake", &RepositoryAnalysis{PrimaryLanguage: "java"}, &UserBuildConfig{RuntimeVersion: "8"})
	if resJava8.Status != VersionUnsupported {
		t.Errorf("expected VersionUnsupported for Java 8, got %s", resJava8.Status)
	}
}

func TestLegacyRuntime_SecurityPolicyToggle(t *testing.T) {
	policy := NewLegacyRuntimePolicy()
	// Disable legacy runtime support via security policy
	policy.SetAllowLegacy(false)

	status, _, reason := policy.EvaluateVersion("python", "3.5")
	if status != VersionUnsupported {
		t.Errorf("expected VersionUnsupported when legacy support disabled, got %s", status)
	}
	if !strings.Contains(reason, "disabled by security policy") {
		t.Errorf("expected security policy rejection reason, got %s", reason)
	}
}

func TestLegacyRuntime_PlanBuildRejection(t *testing.T) {
	planner := NewBuildPlanner()
	tmpDir := t.TempDir()

	// Requesting an unsupported version via PlanBuild returns actionable error
	_, err := planner.PlanBuild(context.Background(), "test-sbx", tmpDir, &UserBuildConfig{
		Language:       "python",
		RuntimeVersion: "3.4",
	})
	if err == nil {
		t.Fatalf("expected PlanBuild to fail for unsupported Python 3.4")
	}
	if !strings.Contains(err.Error(), "obsolete legacy version") && !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected actionable error message, got: %v", err)
	}
}
