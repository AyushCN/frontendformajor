package orchestrator

// ExecutionMode defines the target operational model for code execution.
type ExecutionMode string

const (
	ExecutionModeScript            ExecutionMode = "SCRIPT"
	ExecutionModeInteractiveScript ExecutionMode = "INTERACTIVE_SCRIPT"
	ExecutionModeApplication       ExecutionMode = "APPLICATION"
	ExecutionModeAmbiguous         ExecutionMode = "AMBIGUOUS"
	ExecutionModeUnsupported       ExecutionMode = "UNSUPPORTED"
)

// EntryPointCandidate represents a potential executable file discovered in the repository.
type EntryPointCandidate struct {
	RelativePath     string          `json:"relative_path"`
	Language         string          `json:"language"`
	ExecutionMode    ExecutionMode   `json:"execution_mode"`
	Confidence       ConfidenceLevel `json:"confidence"`
	BuildCommand     string          `json:"build_command,omitempty"`
	SuggestedCommand string          `json:"suggested_command"`
	Score            int             `json:"score"`
	Reason           string          `json:"reason"`
	Framework        string          `json:"framework,omitempty"`
	HasInteractive   bool            `json:"has_interactive,omitempty"`
	RequiresCompile  bool            `json:"requires_compile,omitempty"`
}

// EntryPointResolution captures the ranked candidates, selected start command, and execution mode.
type EntryPointResolution struct {
	PrimaryCandidate  *EntryPointCandidate  `json:"primary_candidate,omitempty"`
	Candidates        []EntryPointCandidate `json:"candidates"`
	BuildCommand      string                `json:"build_command,omitempty"`
	SelectedCommand   string                `json:"selected_command"`
	CommandSource     string                `json:"command_source"` // "USER_EXPLICIT", "PROJECT_CONFIG", "FRAMEWORK_CONVENTION", "CONVENTIONAL_FILENAME", "SINGLE_FILE_INFERENCE", "NATIVE_COMPILATION", "AMBIGUOUS"
	ExecutionMode     ExecutionMode         `json:"execution_mode"`
	Confidence        ConfidenceLevel       `json:"confidence"`
	IsAmbiguous       bool                  `json:"is_ambiguous"`
	DetectedLanguages []DetectedLanguage   `json:"detected_languages,omitempty"`
	Warnings          []string              `json:"warnings,omitempty"`
}
