package security

import (
	"testing"
)

func TestIsProtectedEnvFile(t *testing.T) {
	protected := []string{
		".env",
		".env.local",
		".env.development.local",
		".env.production",
		".env.production.local",
		".env.staging",
		"backend/.env",
		"sub/folder/.env.local",
		".env.test.local",
	}

	for _, p := range protected {
		if !IsProtectedEnvFile(p) {
			t.Errorf("expected %q to be protected, got false", p)
		}
	}

	notProtected := []string{
		".env.example",
		".env.sample",
		".env.template",
		".env.dist",
		"backend/.env.example",
		"docker-compose.yml",
		"main.go",
		"environment.ts",
		"package.json",
		"env.d.ts",
	}

	for _, np := range notProtected {
		if IsProtectedEnvFile(np) {
			t.Errorf("expected %q to NOT be protected, got true", np)
		}
	}
}

func TestIsTemplateEnvFile(t *testing.T) {
	templates := []string{
		".env.example",
		".env.sample",
		".env.template",
		".env.dist",
		"config/.env.example",
	}

	for _, tmpl := range templates {
		if !IsTemplateEnvFile(tmpl) {
			t.Errorf("expected %q to be detected as template, got false", tmpl)
		}
	}

	nonTemplates := []string{
		".env",
		".env.local",
		"main.py",
		"example.go",
	}

	for _, nt := range nonTemplates {
		if IsTemplateEnvFile(nt) {
			t.Errorf("expected %q to NOT be template, got true", nt)
		}
	}
}

func TestMaskSecretValue(t *testing.T) {
	if MaskSecretValue("") != "" {
		t.Errorf("expected empty string for empty secret")
	}
	if MaskSecretValue("short") != "••••••••" {
		t.Errorf("expected fixed dots for short secret, got %q", MaskSecretValue("short"))
	}
	masked := MaskSecretValue("supersecretpassword123")
	if len(masked) == 0 || masked[:2] != "su" {
		t.Errorf("expected prefix 'su', got %q", masked)
	}
}

func TestScanContentForSecrets(t *testing.T) {
	srcWithSecrets := `
package main

const (
    // safe comment: AKIA1111111111111111
    AWS_KEY = "AKIAIOSFODNN7EXAMPLE"
    GH_TOKEN = "ghp_123456789012345678901234567890123456"
)

func run() {
    privKey := "-----BEGIN RSA PRIVATE KEY-----\nMIIE..."
}
`
	findings := ScanContentForSecrets(srcWithSecrets)
	if len(findings) < 3 {
		t.Fatalf("expected at least 3 findings, got %d: %+v", len(findings), findings)
	}

	cleanSrc := `
package main

const (
    AWS_KEY = "your_aws_key_here"
    PORT = 8080
    HOST = "127.0.0.1"
)
`
	cleanFindings := ScanContentForSecrets(cleanSrc)
	if len(cleanFindings) != 0 {
		t.Fatalf("expected 0 findings for clean file, got %d: %+v", len(cleanFindings), cleanFindings)
	}
}
