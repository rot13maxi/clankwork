package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Confidence thresholds for labeling computed verification confidence.
const (
	ConfidenceThresholdHigh   = 0.85
	ConfidenceThresholdMedium = 0.65
)

const (
	RiskLevelNormal = "normal"
	RiskLevelHigh   = "high"
)

type AcceptanceSpecValidationResult struct {
	Valid         bool     `json:"valid"`
	StrengthScore int      `json:"strength_score"`
	RiskLevel     string   `json:"risk_level"`
	Errors        []string `json:"errors,omitempty"`
}

type VerificationReportValidationResult struct {
	Valid              bool     `json:"valid"`
	ProbeCoverage      float64  `json:"probe_coverage"`
	ComputedConfidence float64  `json:"computed_confidence"`
	ConfidenceLabel    string   `json:"confidence_label"`
	ComputedVerdict    string   `json:"computed_verdict"`
	Errors             []string `json:"errors,omitempty"`
}

// ConfidenceLabel returns the string label for a computed confidence score.
func ConfidenceLabel(score float64) string {
	if score >= ConfidenceThresholdHigh {
		return "high"
	}
	if score >= ConfidenceThresholdMedium {
		return "medium"
	}
	return "low"
}

func RequiredConfidenceLabel(riskLevel string) string {
	if normalizeRiskLevel(riskLevel) == RiskLevelHigh {
		return "high"
	}
	return "medium"
}

func MeetsRequiredConfidence(score float64, riskLevel string) bool {
	switch RequiredConfidenceLabel(riskLevel) {
	case "high":
		return score >= ConfidenceThresholdHigh
	default:
		return score >= ConfidenceThresholdMedium
	}
}

func normalizeRiskLevel(riskLevel string) string {
	switch strings.ToLower(strings.TrimSpace(riskLevel)) {
	case "", RiskLevelNormal:
		return RiskLevelNormal
	case RiskLevelHigh:
		return RiskLevelHigh
	default:
		return strings.ToLower(strings.TrimSpace(riskLevel))
	}
}

func (p *AcceptanceProbe) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		p.ID = stableProbeID(text)
		p.Description = text
		return nil
	}
	type alias AcceptanceProbe
	var probe alias
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	*p = AcceptanceProbe(probe)
	return nil
}

func stableProbeID(text string) string {
	text = strings.TrimSpace(strings.ToLower(text))
	var sb strings.Builder
	lastUnderscore := false
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore && sb.Len() > 0 {
			sb.WriteByte('_')
			lastUnderscore = true
		}
	}
	id := strings.Trim(sb.String(), "_")
	if id == "" {
		return "probe"
	}
	return id
}

// ComputeVerificationConfidence computes a deterministic confidence score (0.0-1.0)
// for a verification outcome using signals from the spec, report, and task.
//
// Signal weights (applied in order, each contributing to the final score):
//
//  1. Evidence coverage (0.35): fraction of spec probes with corresponding evidence.
//     Each probe that has at least one matching evidence item for its criterion counts.
//
//  2. Artifact coverage (0.30): fraction of required artifact types from the spec
//     that appear as evidence types in passing results.
//
//  3. Failure penalty (0.15): fraction of criteria that passed (not failed).
//
//  4. Retry penalty (0.10): decreases linearly from 1.0 (retry=0) to 0.0 (retry>=3).
//     High retry counts suggest fragility.
//
//  5. Diversity bonus (0.10): distinct evidence types across all results, capped at 4.
//     More evidence sources = more confidence the result is real.
//
// The function is deterministic: given the same inputs, it always returns the same result.
// It operates entirely on control-plane data structures, never calling an LLM.
func ComputeVerificationConfidence(spec *AcceptanceSpec, report *VerificationReport, retryCount int) float64 {
	if spec == nil || report == nil {
		return 0.0
	}
	if len(spec.Criteria) == 0 || len(report.Results) == 0 {
		return 0.0
	}

	criteria := criteriaByID(spec)
	_ = criteria // used for lookups below
	totalCriteria := float64(len(spec.Criteria))

	// Collect all evidence by criterion and overall.
	evidenceTypes := make(map[string]bool)
	evidenceByCriterion := make(map[string]map[string]bool)
	evidenceByProbe := make(map[string]bool)
	for _, result := range report.Results {
		if evidenceByCriterion[result.CriterionID] == nil {
			evidenceByCriterion[result.CriterionID] = make(map[string]bool)
		}
		for _, ev := range result.Evidence {
			evidenceTypes[ev.Type] = true
			evidenceByCriterion[result.CriterionID][ev.Type] = true
			if ev.ProbeID != "" {
				evidenceByProbe[result.CriterionID+"::"+ev.ProbeID] = true
			}
		}
	}

	// 1. Evidence coverage (0.35): fraction of probes with corresponding evidence.
	// A probe is "covered" only when evidence explicitly links to its probe_id.
	probesCovered := 0
	probesTotal := 0
	for _, criterion := range spec.Criteria {
		for _, probe := range criterion.Probes {
			probesTotal++
			if evidenceByProbe[criterion.ID+"::"+probe.ID] {
				probesCovered++
			}
		}
	}
	evidenceCoverage := 0.0
	if probesTotal > 0 {
		evidenceCoverage = float64(probesCovered) / float64(probesTotal)
	}

	// 2. Artifact coverage (0.30): fraction of required artifact types present.
	requiredArtifactTypes := make(map[string]bool)
	for _, criterion := range spec.Criteria {
		for _, art := range criterion.RequiredArtifacts {
			requiredArtifactTypes[art] = true
		}
	}
	artifactCoverage := 0.0
	if len(requiredArtifactTypes) > 0 {
		matched := 0
		for art := range requiredArtifactTypes {
			if evidenceTypes[art] {
				matched++
			}
		}
		artifactCoverage = float64(matched) / float64(len(requiredArtifactTypes))
	}

	// 3. Failure penalty (0.15): fraction of criteria that passed.
	passedCriteria := 0
	for _, criterion := range spec.Criteria {
		failed := false
		for _, result := range report.Results {
			if result.CriterionID == criterion.ID && result.Status == "fail" {
				failed = true
			}
		}
		if !failed && len(report.Failures) > 0 {
			for _, f := range report.Failures {
				if f.CriterionID == criterion.ID {
					failed = true
				}
			}
		}
		if !failed {
			passedCriteria++
		}
	}
	failureScore := float64(passedCriteria) / totalCriteria

	// 4. Retry penalty (0.10): linear from 1.0 (retry=0) to 0.0 (retry>=3).
	retryScore := 1.0
	if retryCount > 0 {
		retryScore = 1.0 - float64(retryCount)/3.0
		if retryScore < 0 {
			retryScore = 0
		}
	}

	// 5. Diversity bonus (0.10): distinct evidence types, capped at 4.
	diversityScore := 0.0
	distinctTypes := len(evidenceTypes)
	if distinctTypes > 4 {
		distinctTypes = 4
	}
	diversityScore = float64(distinctTypes) / 4.0

	// Weighted combination.
	coverage := evidenceCoverage
	score := 0.35*coverage + 0.30*artifactCoverage + 0.15*failureScore + 0.10*retryScore + 0.10*diversityScore

	// Clamp to [0, 1].
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	return score
}

func ProbeCoverage(spec *AcceptanceSpec, report *VerificationReport) float64 {
	if spec == nil || report == nil {
		return 0
	}
	total := 0
	covered := 0
	evidenceByProbe := make(map[string]bool)
	for _, result := range report.Results {
		for _, ev := range result.Evidence {
			if ev.ProbeID != "" {
				evidenceByProbe[result.CriterionID+"::"+ev.ProbeID] = true
			}
		}
	}
	for _, criterion := range spec.Criteria {
		for _, probe := range criterion.Probes {
			total++
			if evidenceByProbe[criterion.ID+"::"+probe.ID] {
				covered++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(covered) / float64(total)
}

func ComputeEffectiveRisk(spec *AcceptanceSpec, task *Task, bundle *DoneBundle) string {
	return ComputeEffectiveRiskWithPolicy(spec, task, bundle, nil)
}

func ComputeEffectiveRiskWithPolicy(spec *AcceptanceSpec, task *Task, bundle *DoneBundle, policy *AcceptanceRiskPolicy) string {
	effective := RiskLevelNormal
	raise := func(risk string) {
		if normalizeRiskLevel(risk) == RiskLevelHigh {
			effective = RiskLevelHigh
		}
	}
	if spec != nil {
		raise(spec.RiskLevel)
		for _, criterion := range spec.Criteria {
			raise(criterion.RiskLevel)
			if highRiskText(criterion.ID + " " + criterion.Description) {
				effective = RiskLevelHigh
			}
		}
	}
	if task != nil && highRiskText(task.Title+" "+task.Body+" "+task.Template+" "+task.Role) {
		effective = RiskLevelHigh
	}
	if bundle != nil {
		for _, path := range bundle.FilesChanged {
			if highRiskPath(path) || highRiskText(path) || policyHighRiskPath(path, policy) {
				effective = RiskLevelHigh
			}
		}
		for _, test := range bundle.TestsRun {
			if highRiskText(test) {
				effective = RiskLevelHigh
			}
		}
	}
	if policy != nil {
		text := ""
		if task != nil {
			text += " " + task.Title + " " + task.Body + " " + task.Template + " " + task.Role
		}
		if spec != nil {
			for _, criterion := range spec.Criteria {
				text += " " + criterion.ID + " " + criterion.Description + " " + criterion.RiskLevel
			}
		}
		if policyHighRiskText(text, policy) {
			effective = RiskLevelHigh
		}
	}
	return effective
}

func highRiskText(text string) bool {
	lower := strings.ToLower(text)
	terms := []string{
		"auth", "authentication", "authorization", "login", "password", "permission", "permissions",
		"payment", "payments", "billing", "invoice", "delete", "deletion", "migration", "migrations",
		"infra", "iam", "public api", "public-api", "api contract",
	}
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func highRiskPath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	prefixes := []string{
		"internal/auth/",
		"internal/billing/",
		"migrations/",
		"infra/",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func policyHighRiskText(text string, policy *AcceptanceRiskPolicy) bool {
	if policy == nil {
		return false
	}
	lower := strings.ToLower(text)
	for _, label := range policy.HighRiskLabels {
		label = strings.ToLower(strings.TrimSpace(label))
		if label == "" {
			continue
		}
		if strings.Contains(lower, label) || strings.Contains(lower, strings.ReplaceAll(label, "-", " ")) {
			return true
		}
	}
	return false
}

func policyHighRiskPath(candidate string, policy *AcceptanceRiskPolicy) bool {
	if policy == nil {
		return false
	}
	candidate = strings.ToLower(strings.Trim(filepath.ToSlash(strings.TrimSpace(candidate)), "/"))
	for _, pattern := range policy.HighRiskPaths {
		pattern = strings.ToLower(strings.Trim(filepath.ToSlash(strings.TrimSpace(pattern)), "/"))
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if candidate == prefix || strings.HasPrefix(candidate, prefix+"/") {
				return true
			}
			continue
		}
		if strings.Contains(pattern, "*") {
			if ok, _ := path.Match(pattern, candidate); ok {
				return true
			}
			continue
		}
		if candidate == pattern || strings.HasPrefix(candidate, strings.TrimSuffix(pattern, "/")+"/") {
			return true
		}
	}
	return false
}

// VerifyConfidenceBreakdown returns a human-readable summary of the confidence
// computation for audit logs and retry context.
func VerifyConfidenceBreakdown(spec *AcceptanceSpec, report *VerificationReport, retryCount int) string {
	var sb strings.Builder

	if spec == nil || report == nil {
		return "no spec or report available"
	}

	totalEvidence := 0
	evidenceTypes := make(map[string]bool)
	for _, result := range report.Results {
		totalEvidence += len(result.Evidence)
		for _, ev := range result.Evidence {
			evidenceTypes[ev.Type] = true
		}
	}

	passedCriteria := 0
	for _, criterion := range spec.Criteria {
		failed := false
		for _, result := range report.Results {
			if result.CriterionID == criterion.ID && result.Status == "fail" {
				failed = true
			}
		}
		if !failed {
			passedCriteria++
		}
	}

	sb.WriteString(fmt.Sprintf("criteria: %d/%d passed", passedCriteria, len(spec.Criteria)))
	sb.WriteString(fmt.Sprintf(", evidence: %d items (%d types)", totalEvidence, len(evidenceTypes)))
	sb.WriteString(fmt.Sprintf(", retries: %d", retryCount))

	return sb.String()
}

// IsLearningPromotable is the strict legacy promotion gate. Durable learnings
// are only promoted from workflows that:
//  1. Merged (status "merged")
//  2. Have passing verification (verdict "pass")
//  3. Have "high" computed confidence (>= 0.85)
//
// Other extracted learnings should be stored as candidates or failure-pattern
// observations, not promoted rules.
func IsLearningPromotable(taskStatus string, verificationVerdict string, computedConfidence float64) bool {
	// Must be merged.
	if taskStatus != "merged" {
		return false
	}
	// Must have passing verification.
	if verificationVerdict != "pass" {
		return false
	}
	// Must have high computed confidence.
	if ConfidenceLabel(computedConfidence) != "high" {
		return false
	}
	return true
}

func IsWorkflowLearningEligible(input LearningEligibilityInput) bool {
	if !input.FinalOutcomeMerged {
		return false
	}
	if input.VerificationVerdict != "pass" {
		return false
	}
	if ConfidenceLabel(input.ComputedConfidence) == "low" {
		return false
	}
	threshold := input.RetryThreshold
	if threshold <= 0 {
		threshold = 3
	}
	if input.RetryCount >= threshold {
		return false
	}
	if input.UnresolvedAcceptanceAmbiguity || input.ArtifactProvenanceViolation || input.ManualOverrideRequired {
		return false
	}
	return true
}

func ValidateAcceptanceSpecDetailed(spec *AcceptanceSpec, taskID string, task *Task) AcceptanceSpecValidationResult {
	return ValidateAcceptanceSpecDetailedWithPolicy(spec, taskID, task, nil)
}

func ValidateAcceptanceSpecDetailedWithPolicy(spec *AcceptanceSpec, taskID string, task *Task, policy *AcceptanceRiskPolicy) AcceptanceSpecValidationResult {
	riskLevel := ComputeEffectiveRiskWithPolicy(spec, task, nil, policy)
	return validateAcceptanceSpecDetailedForRisk(spec, taskID, riskLevel)
}

func validateAcceptanceSpecDetailedForRisk(spec *AcceptanceSpec, taskID, riskLevel string) AcceptanceSpecValidationResult {
	score := AcceptanceSpecStrengthScore(spec, riskLevel)
	result := AcceptanceSpecValidationResult{
		Valid:         true,
		StrengthScore: score,
		RiskLevel:     riskLevel,
	}
	if err := validateAcceptanceSpec(spec, taskID, riskLevel); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, err.Error())
	}
	minScore := 3
	if riskLevel == RiskLevelHigh {
		minScore = 5
	}
	if score < minScore {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("acceptance spec strength score %d below minimum %d for %s risk", score, minScore, riskLevel))
	}
	return result
}

func ComputeVerdict(spec *AcceptanceSpec, bundle *DoneBundle, report *VerificationReport, artifacts []*Artifact, task *Task) Verdict {
	return ComputeVerdictWithPolicy(spec, bundle, report, artifacts, task, nil)
}

func ComputeVerdictWithPolicy(spec *AcceptanceSpec, bundle *DoneBundle, report *VerificationReport, artifacts []*Artifact, task *Task, policy *AcceptanceRiskPolicy) Verdict {
	taskID := ""
	if task != nil {
		taskID = task.ID
	}
	if taskID == "" && spec != nil {
		taskID = spec.TaskID
	}
	if taskID == "" && report != nil {
		taskID = report.TaskID
	}
	effectiveRisk := ComputeEffectiveRiskWithPolicy(spec, task, bundle, policy)
	specResult := validateAcceptanceSpecDetailedForRisk(spec, taskID, effectiveRisk)
	if !specResult.Valid {
		return Verdict{Status: "reject", Reason: "weak_acceptance_spec", RiskLevel: effectiveRisk, ValidationErrors: specResult.Errors}
	}
	if err := ValidateDoneBundle(bundle, taskID, spec); err != nil {
		return Verdict{Status: "reject", Reason: "invalid_done_bundle", RiskLevel: effectiveRisk, ValidationErrors: []string{err.Error()}}
	}
	pass, err := ValidateVerificationReport(report, taskID, spec)
	if err != nil {
		return Verdict{Status: "reject", Reason: "invalid_verification_report", RiskLevel: effectiveRisk, ValidationErrors: []string{err.Error()}}
	}
	if err := ValidateArtifactReferences(report, artifacts); err != nil {
		return Verdict{Status: "reject", Reason: "invalid_artifact_provenance", RiskLevel: effectiveRisk, ValidationErrors: []string{err.Error()}}
	}
	if err := ValidateArtifactHashes(artifacts); err != nil {
		return Verdict{Status: "reject", Reason: "invalid_artifact_provenance", RiskLevel: effectiveRisk, ValidationErrors: []string{err.Error()}}
	}
	retryCount := 0
	if task != nil {
		retryCount = task.RetryCount
	}
	confidence := ComputeVerificationConfidence(spec, report, retryCount)
	label := ConfidenceLabel(confidence)
	verdict := Verdict{
		RiskLevel:          effectiveRisk,
		ComputedConfidence: confidence,
		ConfidenceLabel:    label,
	}
	if !pass {
		verdict.Status = "fail"
		verdict.Reason = "acceptance_failed"
		return verdict
	}
	if !MeetsRequiredConfidence(confidence, effectiveRisk) {
		verdict.Status = "retry_or_escalate"
		verdict.Reason = "low_confidence"
		return verdict
	}
	if !AdversarialCheckPassed(report, effectiveRisk) {
		verdict.Status = "retry_or_escalate"
		verdict.Reason = "adversarial_check_failed"
		return verdict
	}
	verdict.Status = "pass"
	return verdict
}

func AcceptanceSpecStrengthScore(spec *AcceptanceSpec, riskLevel string) int {
	if spec == nil {
		return 0
	}
	score := 0
	for _, criterion := range spec.Criteria {
		if len(criterion.Probes) > 0 {
			score++
		}
		if len(criterion.RequiredArtifacts) > 0 {
			score++
		}
		if len(criterion.FailIf) > 0 {
			score++
		} else {
			score -= 3
		}
		if criterionHasNegativeAssertion(criterion) {
			score++
		}
		if criterionHasStateTransition(criterion) {
			score++
		}
		if hasIndependentEvidenceSource(criterion) {
			score++
		}
		if criterionOnlyChecksStatusCode(criterion) {
			score -= 2
		}
		if !criterionHasProbeEvidenceMapping(criterion) {
			score -= 2
		}
	}
	return score
}

func ValidateAcceptanceSpec(spec *AcceptanceSpec, taskID string) error {
	return validateAcceptanceSpec(spec, taskID, ComputeEffectiveRisk(spec, nil, nil))
}

func validateAcceptanceSpec(spec *AcceptanceSpec, taskID, effectiveRisk string) error {
	if spec == nil {
		return fmt.Errorf("acceptance_spec required")
	}
	if spec.TaskID != taskID {
		return fmt.Errorf("acceptance_spec.task_id must be %q", taskID)
	}
	if spec.RiskLevel != "" && normalizeRiskLevel(spec.RiskLevel) != RiskLevelNormal && normalizeRiskLevel(spec.RiskLevel) != RiskLevelHigh {
		return fmt.Errorf("acceptance_spec.risk_level must be normal or high")
	}
	if len(spec.Criteria) == 0 {
		return fmt.Errorf("acceptance_spec.criteria must not be empty")
	}
	seen := map[string]bool{}
	for i, c := range spec.Criteria {
		if c.ID == "" {
			return fmt.Errorf("acceptance_spec.criteria[%d].id required", i)
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate criterion id %q", c.ID)
		}
		seen[c.ID] = true
		if c.Description == "" {
			return fmt.Errorf("criterion %s description required", c.ID)
		}
		if c.RiskLevel != "" && normalizeRiskLevel(c.RiskLevel) != RiskLevelNormal && normalizeRiskLevel(c.RiskLevel) != RiskLevelHigh {
			return fmt.Errorf("criterion %s risk_level must be normal or high", c.ID)
		}
		if len(c.Probes) == 0 {
			return fmt.Errorf("criterion %s requires at least one probe", c.ID)
		}
		requiredArtifactSet := map[string]bool{}
		for _, artifactType := range c.RequiredArtifacts {
			requiredArtifactSet[artifactType] = true
		}
		seenProbe := map[string]bool{}
		hasTransition := false
		hasObservableSideEffect := false
		hasAdversarialCheck := false
		for j, probe := range c.Probes {
			if strings.TrimSpace(probe.ID) == "" {
				return fmt.Errorf("criterion %s probes[%d].id required", c.ID, j)
			}
			if seenProbe[probe.ID] {
				return fmt.Errorf("criterion %s duplicate probe id %q", c.ID, probe.ID)
			}
			seenProbe[probe.ID] = true
			if strings.TrimSpace(probe.Description) == "" {
				return fmt.Errorf("criterion %s probe %s requires description", c.ID, probe.ID)
			}
			if strings.TrimSpace(probe.Type) != "" && !validArtifactTypeRE.MatchString(probe.Type) {
				return fmt.Errorf("criterion %s probe %s type %q is malformed; use lowercase words separated by single underscores", c.ID, probe.ID, probe.Type)
			}
			if strings.TrimSpace(probe.Command) == "" {
				return fmt.Errorf("criterion %s probe %s requires executable command", c.ID, probe.ID)
			}
			if len(probe.RequiredEvidence) == 0 {
				return fmt.Errorf("criterion %s probe %s requires required_evidence mapping", c.ID, probe.ID)
			}
			for _, requiredEvidence := range probe.RequiredEvidence {
				requiredEvidence = strings.TrimSpace(requiredEvidence)
				if requiredEvidence == "" {
					return fmt.Errorf("criterion %s probe %s has empty required_evidence type", c.ID, probe.ID)
				}
				if placeholder := firstUnboundPlaceholder(requiredEvidence); placeholder != "" {
					return fmt.Errorf("criterion %s probe %s required_evidence contains unbound placeholder %q", c.ID, probe.ID, placeholder)
				}
				if !validArtifactTypeRE.MatchString(requiredEvidence) {
					return fmt.Errorf("criterion %s probe %s required_evidence type %q is malformed; use lowercase words separated by single underscores", c.ID, probe.ID, requiredEvidence)
				}
				if !requiredArtifactSet[requiredEvidence] {
					return fmt.Errorf("criterion %s probe %s required_evidence %q is not listed in required_artifacts", c.ID, probe.ID, requiredEvidence)
				}
			}
			if placeholder := firstUnboundPlaceholder(probeText(probe)); placeholder != "" {
				return fmt.Errorf("criterion %s probe %s contains unbound placeholder %q", c.ID, probe.ID, placeholder)
			}
			if unsupported := unsupportedProbeCommand(probe.Command); unsupported != "" {
				return fmt.Errorf("criterion %s probe %s uses unsupported or unverified command surface: %s", c.ID, probe.ID, unsupported)
			}
			if weak := weakStateTransition(probe); weak != "" {
				return fmt.Errorf("criterion %s probe %s has weak state transition: %s", c.ID, probe.ID, weak)
			}
			if isShallowProbe(probe) {
				return fmt.Errorf("criterion %s probe %s is too shallow; require an executable assertion, not status-only inspection", c.ID, probe.ID)
			}
			if strings.TrimSpace(probe.Before) != "" && strings.TrimSpace(probe.After) != "" {
				hasTransition = true
			}
			if strings.TrimSpace(probe.ObservableSideEffect) != "" || hasObservableArtifact(c.RequiredArtifacts) {
				hasObservableSideEffect = true
			}
			if strings.TrimSpace(probe.NegativeAssertion) != "" || containsNegativeAssertion(probe.Description+" "+probe.Command) {
				hasAdversarialCheck = true
			}
		}
		if len(c.RequiredArtifacts) == 0 {
			return fmt.Errorf("criterion %s requires at least one required artifact", c.ID)
		}
		for _, artifactType := range c.RequiredArtifacts {
			artifactType = strings.TrimSpace(artifactType)
			if artifactType == "" {
				return fmt.Errorf("criterion %s has empty required artifact type", c.ID)
			}
			if placeholder := firstUnboundPlaceholder(artifactType); placeholder != "" {
				return fmt.Errorf("criterion %s required artifact type contains unbound placeholder %q", c.ID, placeholder)
			}
			if !validArtifactTypeRE.MatchString(artifactType) {
				return fmt.Errorf("criterion %s required artifact type %q is malformed; use lowercase words separated by single underscores", c.ID, artifactType)
			}
		}
		if len(c.FailIf) == 0 {
			return fmt.Errorf("criterion %s requires at least one fail_if condition", c.ID)
		}
		for _, failIf := range c.FailIf {
			if placeholder := firstUnboundPlaceholder(failIf); placeholder != "" {
				return fmt.Errorf("criterion %s fail_if contains unbound placeholder %q", c.ID, placeholder)
			}
			if isNarrativeOnlyNegativeAssertion(failIf) {
				return fmt.Errorf("criterion %s fail_if %q is narrative-only; use a machine-checkable failure condition", c.ID, failIf)
			}
			if unsupported := unsupportedProbeCommand(failIf); unsupported != "" {
				return fmt.Errorf("criterion %s fail_if %q uses unsupported or unverified command surface: %s", c.ID, failIf, unsupported)
			}
			if containsNegativeAssertion(failIf) {
				hasAdversarialCheck = true
				break
			}
		}
		if !hasAdversarialCheck {
			return fmt.Errorf("criterion %s requires an adversarial or negative assertion", c.ID)
		}
		if (c.RequiresStateTransition || effectiveRisk == RiskLevelHigh) && !hasTransition {
			return fmt.Errorf("criterion %s requires a before/after state transition probe", c.ID)
		}
		if c.RequiresNegativeAssertion && !hasAdversarialCheck {
			return fmt.Errorf("criterion %s requires a negative assertion", c.ID)
		}
		if !hasObservableSideEffect {
			return fmt.Errorf("criterion %s requires an observable side effect", c.ID)
		}
	}
	return nil
}

var anglePlaceholderRE = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9_-]*)>`)
var validArtifactTypeRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

var placeholderHashPatterns = []string{
	"test",
	"todo",
	"placeholder",
	"example",
	"abc",
	"0000000000000000000000000000000000000000000000000000000000000000",
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
}

func probeText(probe AcceptanceProbe) string {
	return strings.Join([]string{
		probe.Type,
		probe.Description,
		probe.Command,
		strings.Join(probe.RequiredEvidence, " "),
		probe.Before,
		probe.After,
		probe.ObservableSideEffect,
		probe.NegativeAssertion,
	}, " ")
}

func firstUnboundPlaceholder(text string) string {
	for _, match := range anglePlaceholderRE.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		name := strings.ToLower(match[1])
		switch name {
		case "task-id", "task_id", "branch", "old-id", "old_id", "new-id", "new_id", "id", "name", "repo", "path", "artifact-type", "artifact_type":
			return match[0]
		}
	}
	return ""
}

func unsupportedProbeCommand(command string) string {
	command = strings.TrimSpace(command)
	lower := strings.ToLower(command)
	if strings.Contains(lower, "clankwork ") && strings.Contains(lower, "--format json") {
		return "clankwork --format json is not part of the stable CLI contract"
	}
	if strings.Contains(command, "/Users/") || strings.Contains(command, "/home/") {
		return "probe commands must be worktree-relative, not hard-coded to a developer checkout"
	}
	if strings.Contains(command, "FETCH_HEAD") {
		return "FETCH_HEAD is volatile; use an explicit branch, commit, or fixture"
	}
	if hasUnquotedPipe(command) && !strings.Contains(lower, "pipefail") {
		return "probe pipelines must enable pipefail so the asserted command cannot be masked by a later stage"
	}
	if strings.Contains(lower, "grep -c") && !hasExplicitCountComparison(lower) {
		return "grep -c probes must explicitly compare the count so exit status proves the assertion"
	}
	if strings.Contains(lower, "pipestatus") && !strings.Contains(lower, "exit ${pipestatus[0]}") {
		return "PIPESTATUS probes must exit with the captured command status, not only echo it"
	}
	if strings.Contains(command, "$?") && strings.Contains(lower, "echo") {
		return "probe commands must exit with assertion status, not only echo $?"
	}
	if masksEarlierCommandFailure(command) {
		return "probe command lists must use set -e or explicit assertions so earlier command failures cannot be masked"
	}
	if strings.Contains(lower, "|| echo") {
		return "probe commands must exit non-zero on failure, not mask failure with echo"
	}
	if strings.HasPrefix(lower, "awk ") || strings.Contains(lower, "; awk ") {
		if !strings.Contains(lower, "end{") {
			return "awk probes must include an END block that exits non-zero when the assertion is not observed"
		}
		if !awkHasNonZeroExit(lower) {
			return "awk probes must exit non-zero from END when the assertion is not observed"
		}
	}
	if strings.Contains(lower, "git diff") && strings.Contains(lower, "--name-only") && !hasExplicitFileListAssertion(lower) {
		return "git diff --name-only probes must assert on the file list, not only print it"
	}
	return ""
}

func hasUnquotedPipe(command string) bool {
	var quote rune
	escaped := false
	for _, r := range command {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		switch r {
		case '\'', '"':
			if quote == 0 {
				quote = r
				continue
			}
			if quote == r {
				quote = 0
				continue
			}
		case '|':
			if quote == 0 {
				return true
			}
		}
	}
	return false
}

func masksEarlierCommandFailure(command string) bool {
	parts := splitUnquotedSemicolon(command)
	if len(parts) <= 1 {
		return false
	}
	if strings.Contains(strings.ToLower(command), "set -e") {
		return false
	}
	nonSetup := 0
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" || part == "set -o pipefail" || part == "set -e" || part == "set -euo pipefail" {
			continue
		}
		nonSetup++
	}
	return nonSetup > 1
}

func splitUnquotedSemicolon(command string) []string {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			current.WriteRune(r)
			escaped = true
			continue
		}
		switch r {
		case '\'', '"':
			if quote == 0 {
				quote = r
			} else if quote == r {
				quote = 0
			}
			current.WriteRune(r)
		case ';':
			if quote == 0 {
				parts = append(parts, current.String())
				current.Reset()
				continue
			}
			current.WriteRune(r)
		default:
			current.WriteRune(r)
		}
	}
	parts = append(parts, current.String())
	return parts
}

func awkHasNonZeroExit(command string) bool {
	nonZeroExits := []string{
		"exit 1",
		"exit(1",
		"?0:1",
		"? 0 : 1",
		"exit !",
	}
	for _, marker := range nonZeroExits {
		if strings.Contains(command, marker) {
			return true
		}
	}
	return false
}

func hasExplicitCountComparison(command string) bool {
	comparisonMarkers := []string{
		" test ",
		"test ",
		"[ ",
		"[[ ",
		"((",
		" -eq ",
		" -ne ",
		" -gt ",
		" -ge ",
		" -lt ",
		" -le ",
	}
	for _, marker := range comparisonMarkers {
		if strings.Contains(command, marker) {
			return true
		}
	}
	return false
}

func hasExplicitFileListAssertion(command string) bool {
	if hasExplicitCountComparison(command) {
		return true
	}
	assertionMarkers := []string{
		"assert len(files)",
		"assert files",
		"assert paths",
		"assert changed",
		"sys.exit(1)",
		"raise systemexit(1)",
	}
	for _, marker := range assertionMarkers {
		if strings.Contains(command, marker) {
			return true
		}
	}
	return false
}

func weakStateTransition(probe AcceptanceProbe) string {
	for label, value := range map[string]string{
		"before": probe.Before,
		"after":  probe.After,
	} {
		lower := strings.ToLower(strings.TrimSpace(value))
		if lower == "" {
			continue
		}
		switch lower {
		case "unknown", "n/a", "na", "none", "tbd", "to be determined":
			return label + " state is not concrete"
		}
		if strings.HasPrefix(lower, "unknown ") || strings.Contains(lower, " may or may not ") {
			return label + " state is not concrete"
		}
	}
	return ""
}

func isNarrativeOnlyNegativeAssertion(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return true
	}
	vague := []string{
		"looks wrong",
		"seems wrong",
		"does not work",
		"bad output",
		"wrong behavior",
		"incorrect behavior",
		"anything fails",
		"something fails",
	}
	for _, phrase := range vague {
		if lower == phrase {
			return true
		}
	}
	narrativePatterns := []string{
		"the note omits",
		"note omits",
		"says nothing",
		"no new content added",
		"not added at all",
		"unchanged from head",
	}
	for _, phrase := range narrativePatterns {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	machineMarkers := []string{"exit", "nonzero", "stderr", "stdout", "status", "http", "code", "contains", "missing", "absent", "equals", "count", "diff", "file", "row", "query", "pid", "socket", "sha256"}
	for _, marker := range machineMarkers {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return !containsNegativeAssertion(lower)
}

func isShallowProbe(probe AcceptanceProbe) bool {
	text := strings.ToLower(strings.TrimSpace(probe.Description + " " + probe.Command))
	if len(strings.Fields(text)) < 3 {
		return true
	}
	statusOnly := []string{
		"status", "show status", "list", "grep", "check logs", "inspect", "look at",
	}
	assertive := []string{
		"assert", "expect", "verify", "prove", "fails", "succeeds", "changes", "contains", "equals", "nonzero", "missing", "removed", "created", "updated",
	}
	for _, word := range assertive {
		if strings.Contains(text, word) {
			return false
		}
	}
	for _, phrase := range statusOnly {
		if text == phrase || strings.HasPrefix(text, phrase+" ") {
			return true
		}
	}
	return false
}

func containsNegativeAssertion(text string) bool {
	text = strings.ToLower(text)
	terms := []string{"fail", "fails", "failure", "not ", " no ", "missing", "absent", "invalid", "reject", "error", "nonzero", "unchanged", "old ", "stale", "forbidden"}
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func criterionHasNegativeAssertion(criterion AcceptanceCriterion) bool {
	for _, probe := range criterion.Probes {
		if strings.TrimSpace(probe.NegativeAssertion) != "" || containsNegativeAssertion(probe.Description+" "+probe.Command) {
			return true
		}
		if strings.EqualFold(probe.Type, "negative_assertion") {
			return true
		}
	}
	for _, failIf := range criterion.FailIf {
		if containsNegativeAssertion(failIf) {
			return true
		}
	}
	return false
}

func criterionHasStateTransition(criterion AcceptanceCriterion) bool {
	for _, probe := range criterion.Probes {
		if strings.TrimSpace(probe.Before) != "" && strings.TrimSpace(probe.After) != "" {
			return true
		}
	}
	return false
}

func hasIndependentEvidenceSource(criterion AcceptanceCriterion) bool {
	types := map[string]bool{}
	for _, artifactType := range criterion.RequiredArtifacts {
		if strings.TrimSpace(artifactType) != "" {
			types[artifactType] = true
		}
	}
	return len(types) >= 2
}

func criterionOnlyChecksStatusCode(criterion AcceptanceCriterion) bool {
	if len(criterion.Probes) == 0 {
		return false
	}
	for _, probe := range criterion.Probes {
		text := strings.ToLower(probe.Description + " " + probe.Command)
		statusOnly := strings.Contains(text, "status code") || strings.Contains(text, "expected_status") || strings.Contains(text, "http 200")
		hasOtherEvidence := strings.TrimSpace(probe.Before) != "" ||
			strings.TrimSpace(probe.After) != "" ||
			strings.TrimSpace(probe.ObservableSideEffect) != "" ||
			strings.TrimSpace(probe.NegativeAssertion) != "" ||
			len(probe.RequiredEvidence) > 1
		if !statusOnly || hasOtherEvidence {
			return false
		}
	}
	return true
}

func criterionHasProbeEvidenceMapping(criterion AcceptanceCriterion) bool {
	if len(criterion.Probes) == 0 {
		return false
	}
	for _, probe := range criterion.Probes {
		if len(probe.RequiredEvidence) == 0 {
			return false
		}
	}
	return true
}

func hasObservableArtifact(required []string) bool {
	observable := []string{"trace", "screenshot", "video", "log", "transcript", "assertion", "diff", "output", "result", "database", "db_", "api_", "file_"}
	for _, artifact := range required {
		lower := strings.ToLower(artifact)
		for _, marker := range observable {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func ValidateDoneBundle(bundle *DoneBundle, taskID string, spec *AcceptanceSpec) error {
	if bundle == nil {
		return fmt.Errorf("done_bundle required")
	}
	if bundle.TaskID != taskID {
		return fmt.Errorf("done_bundle.task_id must be %q", taskID)
	}
	if bundle.Summary == "" {
		return fmt.Errorf("done_bundle.summary required")
	}
	if len(bundle.Claims) == 0 {
		return fmt.Errorf("done_bundle.claims must not be empty")
	}
	if len(bundle.Artifacts) == 0 {
		return fmt.Errorf("done_bundle.artifacts must not be empty")
	}

	artifactTypes := map[string]bool{}
	artifactByCriterion := map[string]map[string]bool{}
	for i, a := range bundle.Artifacts {
		if a.Type == "" {
			return fmt.Errorf("done_bundle.artifacts[%d].type required", i)
		}
		if a.Path == "" {
			return fmt.Errorf("done_bundle.artifacts[%d].path required", i)
		}
		if err := validateArtifactProvenance(fmt.Sprintf("done_bundle.artifacts[%d]", i), a.ProbeID, a.Command, a.ProducerStep, a.ProducerRole, a.Timestamp, a.ContentHash, a.Path, a.Authoritative); err != nil {
			return err
		}
		artifactTypes[a.Type] = true
		if a.CriterionID != "" {
			if artifactByCriterion[a.CriterionID] == nil {
				artifactByCriterion[a.CriterionID] = map[string]bool{}
			}
			artifactByCriterion[a.CriterionID][a.Type] = true
		}
	}

	criteria := criteriaByID(spec)
	for i, claim := range bundle.Claims {
		if claim.CriterionID == "" {
			return fmt.Errorf("done_bundle.claims[%d].criterion_id required", i)
		}
		if claim.Status != "satisfied" {
			return fmt.Errorf("claim %s status must be satisfied", claim.CriterionID)
		}
		if spec != nil {
			criterion, ok := criteria[claim.CriterionID]
			if !ok {
				return fmt.Errorf("claim %s does not match acceptance spec", claim.CriterionID)
			}
			for _, required := range criterion.RequiredArtifacts {
				if !artifactTypes[required] && !artifactByCriterion[claim.CriterionID][required] {
					return fmt.Errorf("claim %s missing required artifact type %q", claim.CriterionID, required)
				}
			}
		}
	}
	return nil
}

func validateArtifactProvenance(name, probeID, command, producerStep, producerRole, timestamp, contentHash, path string, authoritative bool) error {
	if strings.TrimSpace(producerStep) == "" {
		return fmt.Errorf("%s.producer_step required", name)
	}
	if strings.TrimSpace(producerRole) == "" {
		return fmt.Errorf("%s.producer_role required", name)
	}
	if strings.TrimSpace(timestamp) == "" {
		return fmt.Errorf("%s.timestamp required", name)
	}
	if strings.TrimSpace(probeID) == "" && strings.TrimSpace(command) == "" {
		return fmt.Errorf("%s requires probe_id or command linkage", name)
	}
	if strings.TrimSpace(path) != "" && strings.TrimSpace(contentHash) == "" {
		return fmt.Errorf("%s.content_hash required for path artifacts", name)
	}
	if !authoritative {
		return fmt.Errorf("%s.authoritative must be true", name)
	}
	if isPlaceholderHash(contentHash) {
		return fmt.Errorf("%s.content_hash %q is a placeholder value; use the actual sha256 of the artifact content (omit content_hash if the hash is unavailable)", name, contentHash)
	}
	return nil
}

func ValidateVerificationReport(report *VerificationReport, taskID string, spec *AcceptanceSpec) (bool, error) {
	if report == nil {
		return false, fmt.Errorf("verification_report required")
	}
	if report.TaskID != taskID {
		return false, fmt.Errorf("verification_report.task_id must be %q", taskID)
	}
	if len(report.Results) == 0 {
		return false, fmt.Errorf("verification_report.results must not be empty")
	}
	if report.Confidence == "" {
		return false, fmt.Errorf("verification_report.confidence required")
	}
	if report.AdversarialReview != nil {
		if err := ValidateAdversarialReview(report.AdversarialReview, taskID); err != nil {
			return false, err
		}
	}

	pass := len(report.Failures) == 0
	resultByCriterion := map[string]VerificationResult{}
	validProbeByCriterion := map[string]map[string]bool{}
	if spec != nil {
		for _, criterion := range spec.Criteria {
			validProbeByCriterion[criterion.ID] = map[string]bool{}
			for _, probe := range criterion.Probes {
				validProbeByCriterion[criterion.ID][probe.ID] = true
			}
		}
	}
	for i, result := range report.Results {
		if result.CriterionID == "" {
			return false, fmt.Errorf("verification_report.results[%d].criterion_id required", i)
		}
		if result.Status != "pass" && result.Status != "fail" {
			return false, fmt.Errorf("result %s status must be pass or fail", result.CriterionID)
		}
		if result.Status == "pass" && len(result.Evidence) == 0 {
			return false, fmt.Errorf("passing result %s requires evidence", result.CriterionID)
		}
		if result.Status == "fail" {
			pass = false
		}
		for j, ev := range result.Evidence {
			if strings.TrimSpace(ev.ArtifactID) == "" {
				return false, fmt.Errorf("result %s evidence[%d].artifact_id required", result.CriterionID, j)
			}
			if ev.Type == "" {
				return false, fmt.Errorf("result %s evidence[%d].type required", result.CriterionID, j)
			}
			if err := validateArtifactProvenance(fmt.Sprintf("result %s evidence[%d]", result.CriterionID, j), ev.ProbeID, ev.Command, ev.ProducerStep, ev.ProducerRole, ev.Timestamp, ev.ContentHash, ev.Path, ev.Authoritative); err != nil {
				return false, err
			}
			if !isVerifierEvidenceProducer(ev.ProducerRole) {
				return false, fmt.Errorf("result %s evidence[%d] producer_role %q cannot satisfy verifier-required evidence", result.CriterionID, j, ev.ProducerRole)
			}
			if spec != nil && ev.ProbeID != "" && !validProbeByCriterion[result.CriterionID][ev.ProbeID] {
				return false, fmt.Errorf("result %s evidence[%d] references unknown probe_id %q", result.CriterionID, j, ev.ProbeID)
			}
		}
		resultByCriterion[result.CriterionID] = result
	}

	if spec != nil {
		for _, criterion := range spec.Criteria {
			result, ok := resultByCriterion[criterion.ID]
			if !ok {
				return false, fmt.Errorf("criterion %s has no verification result", criterion.ID)
			}
			if result.Status != "pass" {
				pass = false
				continue
			}
			evidenceTypes := map[string]bool{}
			evidenceProbeIDs := map[string]bool{}
			evidenceTypesByProbe := map[string]map[string]bool{}
			for _, ev := range result.Evidence {
				evidenceTypes[ev.Type] = true
				if ev.ProbeID != "" {
					evidenceProbeIDs[ev.ProbeID] = true
					if evidenceTypesByProbe[ev.ProbeID] == nil {
						evidenceTypesByProbe[ev.ProbeID] = map[string]bool{}
					}
					evidenceTypesByProbe[ev.ProbeID][ev.Type] = true
				}
			}
			for _, required := range criterion.RequiredArtifacts {
				if !evidenceTypes[required] {
					return false, fmt.Errorf("criterion %s missing evidence type %q", criterion.ID, required)
				}
			}
			for _, probe := range criterion.Probes {
				if !evidenceProbeIDs[probe.ID] {
					return false, fmt.Errorf("criterion %s probe %s has no linked evidence", criterion.ID, probe.ID)
				}
				for _, requiredEvidence := range probe.RequiredEvidence {
					if !evidenceTypesByProbe[probe.ID][requiredEvidence] {
						return false, fmt.Errorf("criterion %s probe %s missing required evidence type %q", criterion.ID, probe.ID, requiredEvidence)
					}
				}
			}
		}
	}
	return pass, nil
}

func RequiresAdversarialCheck(riskLevel string) bool {
	return normalizeRiskLevel(riskLevel) == RiskLevelHigh
}

func ValidateAdversarialReview(review *AdversarialReview, taskID string) error {
	if review == nil {
		return fmt.Errorf("adversarial_review required")
	}
	if review.TaskID != taskID {
		return fmt.Errorf("adversarial_review.task_id must be %q", taskID)
	}
	for i, finding := range review.AdversarialFindings {
		if strings.TrimSpace(finding.Risk) == "" {
			return fmt.Errorf("adversarial_findings[%d].risk required", i)
		}
		if strings.TrimSpace(finding.SuggestedProbe) == "" {
			return fmt.Errorf("adversarial_findings[%d].suggested_probe required", i)
		}
		switch strings.ToLower(strings.TrimSpace(finding.Severity)) {
		case "low", "medium", "high":
		default:
			return fmt.Errorf("adversarial_findings[%d].severity must be low, medium, or high", i)
		}
	}
	evidenceByProbe := map[string]bool{}
	for i, evidence := range review.FollowupEvidence {
		if strings.TrimSpace(evidence.ArtifactID) == "" {
			return fmt.Errorf("adversarial followup_evidence[%d].artifact_id required", i)
		}
		if strings.TrimSpace(evidence.ProbeID) == "" {
			return fmt.Errorf("adversarial followup_evidence[%d].probe_id required", i)
		}
		evidenceByProbe[strings.TrimSpace(evidence.ProbeID)] = true
		if err := validateArtifactProvenance(fmt.Sprintf("adversarial followup_evidence[%d]", i), evidence.ProbeID, evidence.Command, evidence.ProducerStep, evidence.ProducerRole, evidence.Timestamp, evidence.ContentHash, evidence.Path, evidence.Authoritative); err != nil {
			return err
		}
		if !isVerifierEvidenceProducer(evidence.ProducerRole) {
			return fmt.Errorf("adversarial followup_evidence[%d] producer_role %q cannot satisfy follow-up evidence", i, evidence.ProducerRole)
		}
	}
	for i, probeID := range review.AppendedProbeIDs {
		probeID = strings.TrimSpace(probeID)
		if probeID == "" {
			continue
		}
		if !evidenceByProbe[probeID] && strings.TrimSpace(review.DismissalReason) == "" {
			return fmt.Errorf("adversarial appended_probe_ids[%d].%q missing followup_evidence", i, probeID)
		}
	}
	return nil
}

func AdversarialCheckPassed(report *VerificationReport, riskLevel string) bool {
	if !RequiresAdversarialCheck(riskLevel) {
		return true
	}
	return AdversarialReviewSatisfied(report)
}

func AdversarialReviewSatisfied(report *VerificationReport) bool {
	if report == nil || report.AdversarialReview == nil {
		return false
	}
	if report.AdversarialReview.RequiredFollowup && strings.TrimSpace(report.AdversarialReview.DismissalReason) == "" && len(report.AdversarialReview.FollowupEvidence) == 0 {
		return false
	}
	for _, finding := range report.AdversarialReview.AdversarialFindings {
		if strings.EqualFold(finding.Severity, "high") && strings.TrimSpace(report.AdversarialReview.DismissalReason) == "" && len(report.AdversarialReview.FollowupEvidence) == 0 {
			return false
		}
	}
	return true
}

func AppendAdversarialProbes(spec *AcceptanceSpec, review *AdversarialReview) []string {
	if spec == nil || review == nil || len(review.AdversarialFindings) == 0 || len(spec.Criteria) == 0 {
		return nil
	}
	criterion := &spec.Criteria[0]
	evidenceType := "cli_transcript"
	if len(criterion.RequiredArtifacts) > 0 {
		evidenceType = criterion.RequiredArtifacts[0]
	} else {
		criterion.RequiredArtifacts = append(criterion.RequiredArtifacts, evidenceType)
	}
	existingByDescription := map[string]bool{}
	existingIDs := map[string]bool{}
	for _, probe := range criterion.Probes {
		existingByDescription[strings.TrimSpace(probe.Description)] = true
		existingIDs[probe.ID] = true
	}
	var appended []string
	for i, finding := range review.AdversarialFindings {
		description := strings.TrimSpace(finding.SuggestedProbe)
		if description == "" || existingByDescription[description] {
			continue
		}
		id := fmt.Sprintf("adversarial_%d", i+1)
		for existingIDs[id] {
			id += "_retry"
		}
		criterion.Probes = append(criterion.Probes, AcceptanceProbe{
			ID:                   id,
			Description:          description,
			Type:                 "negative_assertion",
			RequiredEvidence:     []string{evidenceType},
			ObservableSideEffect: "adversarial follow-up evidence",
			NegativeAssertion:    finding.Risk,
		})
		existingByDescription[description] = true
		existingIDs[id] = true
		appended = append(appended, id)
	}
	return appended
}

func isVerifierEvidenceProducer(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "verifier", "acceptance-verifier", "acceptance_verifier", "control-plane", "control_plane":
		return true
	default:
		return false
	}
}

func ValidateVerificationReportDetailed(report *VerificationReport, taskID string, spec *AcceptanceSpec, retryCount int) VerificationReportValidationResult {
	return ValidateVerificationReportDetailedWithPolicy(report, taskID, spec, retryCount, nil)
}

func ValidateVerificationReportDetailedWithPolicy(report *VerificationReport, taskID string, spec *AcceptanceSpec, retryCount int, policy *AcceptanceRiskPolicy) VerificationReportValidationResult {
	result := VerificationReportValidationResult{
		Valid:         true,
		ProbeCoverage: ProbeCoverage(spec, report),
	}
	pass, err := ValidateVerificationReport(report, taskID, spec)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, err.Error())
		result.ComputedVerdict = "reject"
	}
	if pass {
		result.ComputedVerdict = "pass"
	} else if result.ComputedVerdict == "" {
		result.ComputedVerdict = "fail"
	}
	result.ComputedConfidence = ComputeVerificationConfidence(spec, report, retryCount)
	result.ConfidenceLabel = ConfidenceLabel(result.ComputedConfidence)
	if result.Valid && !MeetsRequiredConfidence(result.ComputedConfidence, ComputeEffectiveRiskWithPolicy(spec, nil, nil, policy)) {
		result.ComputedVerdict = "retry_or_escalate"
	}
	return result
}

func ValidateAddArtifactRequest(req AddArtifactRequest) error {
	if strings.TrimSpace(req.TaskID) == "" {
		return fmt.Errorf("task_id required")
	}
	if strings.TrimSpace(req.Producer) == "" {
		return fmt.Errorf("producer required")
	}
	if strings.TrimSpace(req.Path) == "" {
		return fmt.Errorf("path required")
	}
	if strings.TrimSpace(req.ArtifactType) == "" {
		return fmt.Errorf("artifact_type required")
	}
	if !validArtifactTypeRE.MatchString(req.ArtifactType) {
		return fmt.Errorf("artifact_type %q is malformed; use lowercase words separated by single underscores", req.ArtifactType)
	}
	if strings.TrimSpace(req.SHA256) == "" {
		return fmt.Errorf("sha256 required")
	}
	if !strings.HasPrefix(req.SHA256, "sha256:") && len(req.SHA256) != 64 {
		return fmt.Errorf("sha256 must be a 64-character hex digest or sha256:<digest>")
	}
	return nil
}

func ValidateArtifactReferences(report *VerificationReport, artifacts []*Artifact) error {
	if report == nil {
		return nil
	}
	byID := map[string]*Artifact{}
	for _, artifact := range artifacts {
		if artifact != nil {
			byID[artifact.ID] = artifact
		}
	}
	for _, result := range report.Results {
		for j, evidence := range result.Evidence {
			artifact, ok := byID[evidence.ArtifactID]
			if !ok {
				return fmt.Errorf("result %s evidence[%d]: artifact_id %q is not registered in the artifact registry — register the artifact before referencing it in the verification report", result.CriterionID, j, evidence.ArtifactID)
			}
			if artifact.TaskID != report.TaskID {
				return fmt.Errorf("result %s evidence[%d]: artifact_id %q belongs to task %q (expected %q) — use an artifact from the same task", result.CriterionID, j, evidence.ArtifactID, artifact.TaskID, report.TaskID)
			}
			if artifact.Status == "invalidated" {
				return fmt.Errorf("result %s evidence[%d]: artifact_id %q is invalidated — produce fresh evidence and register a new artifact", result.CriterionID, j, evidence.ArtifactID)
			}
			if artifact.ArtifactType != evidence.Type {
				return fmt.Errorf("result %s evidence[%d]: type %q does not match registered artifact type %q — use the artifact's actual type", result.CriterionID, j, evidence.Type, artifact.ArtifactType)
			}
			if evidence.Path != "" && artifact.Path != evidence.Path {
				return fmt.Errorf("result %s evidence[%d]: path %q does not match registered artifact path %q — use the artifact's actual path", result.CriterionID, j, evidence.Path, artifact.Path)
			}
			if evidence.ContentHash != "" && normalizeSHA256(evidence.ContentHash) != normalizeSHA256(artifact.SHA256) {
				return fmt.Errorf("result %s evidence[%d]: content_hash %q does not match registered artifact sha256 %q — use sha256sum of the artifact file content", result.CriterionID, j, evidence.ContentHash, artifact.SHA256)
			}
			if requiresCommandMetadata(evidence.Type) {
				if artifact.Command == "" {
					return fmt.Errorf("result %s evidence[%d]: evidence type %q requires command metadata in the artifact registry, but artifact_id %q has no command recorded — register the artifact with the command that produced it", result.CriterionID, j, evidence.Type, evidence.ArtifactID)
				}
				if evidence.Command != "" && !commandMatches(evidence.Command, artifact.Command) {
					return fmt.Errorf("result %s evidence[%d]: evidence command %q does not match registered artifact command %q — use the actual command that produced the artifact", result.CriterionID, j, evidence.Command, artifact.Command)
				}
			}
		}
	}
	if report.AdversarialReview != nil {
		for j, evidence := range report.AdversarialReview.FollowupEvidence {
			artifact, ok := byID[evidence.ArtifactID]
			if !ok {
				return fmt.Errorf("adversarial followup_evidence[%d]: artifact_id %q is not registered in the artifact registry", j, evidence.ArtifactID)
			}
			if artifact.TaskID != report.TaskID {
				return fmt.Errorf("adversarial followup_evidence[%d]: artifact_id %q belongs to task %q (expected %q)", j, evidence.ArtifactID, artifact.TaskID, report.TaskID)
			}
			if artifact.Status == "invalidated" {
				return fmt.Errorf("adversarial followup_evidence[%d]: artifact_id %q is invalidated", j, evidence.ArtifactID)
			}
			if artifact.ArtifactType != evidence.Type {
				return fmt.Errorf("adversarial followup_evidence[%d] type %q does not match registered artifact type %q", j, evidence.Type, artifact.ArtifactType)
			}
			if evidence.Path != "" && artifact.Path != evidence.Path {
				return fmt.Errorf("adversarial followup_evidence[%d] path %q does not match registered artifact path %q", j, evidence.Path, artifact.Path)
			}
			if evidence.ContentHash != "" && normalizeSHA256(evidence.ContentHash) != normalizeSHA256(artifact.SHA256) {
				return fmt.Errorf("adversarial followup_evidence[%d] content_hash does not match registered artifact sha256", j)
			}
		}
	}
	return nil
}

func ValidateArtifactHashes(artifacts []*Artifact) error {
	if changed := ArtifactHashMismatches(artifacts); len(changed) > 0 {
		return fmt.Errorf("artifact %s hash changed after registration", changed[0].ID)
	}
	return nil
}

func ArtifactHashMismatches(artifacts []*Artifact) []*Artifact {
	var changed []*Artifact
	for _, artifact := range artifacts {
		if artifact == nil {
			continue
		}
		path := artifact.Path
		if artifact.WorkingDirectory != "" && !filepath.IsAbs(path) {
			path = filepath.Join(artifact.WorkingDirectory, path)
		}
		hash, err := fileSHA256(path)
		if err != nil {
			changed = append(changed, artifact)
			continue
		}
		if normalizeSHA256(hash) != normalizeSHA256(artifact.SHA256) {
			changed = append(changed, artifact)
		}
	}
	return changed
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func normalizeSHA256(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
}

// isPlaceholderHash returns true if the content_hash is a well-known placeholder
// value that provides no actual verification guarantee. These are rejected to
// prevent agents from bypassing evidence integrity checks.
func isPlaceholderHash(contentHash string) bool {
	normalized := normalizeSHA256(contentHash)
	if normalized == "" {
		return false
	}
	for _, placeholder := range placeholderHashPatterns {
		if normalized == placeholder {
			return true
		}
	}
	// Reject single-repeated characters (e.g. "aaaa..." or "1111...")
	if len(normalized) >= 16 {
		allSame := true
		first := normalized[0]
		for i := 1; i < len(normalized); i++ {
			if normalized[i] != first {
				allSame = false
				break
			}
		}
		if allSame {
			return true
		}
	}
	// Reject if it looks like a non-hex string disguised as a hash
	if len(normalized) > 0 {
		for _, c := range normalized {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return true
			}
		}
	}
	return false
}

func ValidateExecutionPlanDetailed(plan *VerificationExecutionPlan, spec *AcceptanceSpec) ExecutionPlanValidationResult {
	result := ExecutionPlanValidationResult{Valid: true}
	if err := ValidateExecutionPlan(plan, spec); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, err.Error())
	}
	return result
}

func ValidateExecutionPlan(plan *VerificationExecutionPlan, spec *AcceptanceSpec) error {
	if plan == nil {
		return fmt.Errorf("execution_plan required")
	}
	if strings.TrimSpace(plan.TaskID) == "" {
		return fmt.Errorf("execution_plan.task_id required")
	}
	if spec != nil && plan.TaskID != spec.TaskID {
		return fmt.Errorf("execution_plan.task_id must be %q", spec.TaskID)
	}
	if len(plan.Steps) == 0 {
		return fmt.Errorf("execution_plan.steps must not be empty")
	}
	validProbeIDs := map[string]bool{}
	requiredEvidenceByProbe := map[string]map[string]bool{}
	if spec != nil {
		for _, criterion := range spec.Criteria {
			for _, probe := range criterion.Probes {
				validProbeIDs[probe.ID] = true
				if requiredEvidenceByProbe[probe.ID] == nil {
					requiredEvidenceByProbe[probe.ID] = map[string]bool{}
				}
				for _, evidence := range probe.RequiredEvidence {
					requiredEvidenceByProbe[probe.ID][evidence] = true
				}
			}
		}
	}
	seenSteps := map[string]bool{}
	coveredProbes := map[string]bool{}
	producedByProbe := map[string]map[string]bool{}
	for i, step := range plan.Steps {
		if strings.TrimSpace(step.ID) == "" {
			return fmt.Errorf("execution_plan.steps[%d].id required", i)
		}
		if seenSteps[step.ID] {
			return fmt.Errorf("duplicate execution step id %q", step.ID)
		}
		seenSteps[step.ID] = true
		if strings.TrimSpace(step.ProbeID) == "" {
			return fmt.Errorf("execution step %s probe_id required", step.ID)
		}
		if spec != nil && !validProbeIDs[step.ProbeID] {
			return fmt.Errorf("execution step %s references unknown probe_id %q", step.ID, step.ProbeID)
		}
		if err := validateExecutionStepType(step); err != nil {
			return err
		}
		if len(step.Produces) == 0 {
			return fmt.Errorf("execution step %s produces must not be empty", step.ID)
		}
		if producedByProbe[step.ProbeID] == nil {
			producedByProbe[step.ProbeID] = map[string]bool{}
		}
		for _, produced := range step.Produces {
			produced = strings.TrimSpace(produced)
			if produced == "" {
				return fmt.Errorf("execution step %s has empty produces type", step.ID)
			}
			if !validArtifactTypeRE.MatchString(produced) {
				return fmt.Errorf("execution step %s produces type %q is malformed; use lowercase words separated by single underscores", step.ID, produced)
			}
			producedByProbe[step.ProbeID][produced] = true
		}
		coveredProbes[step.ProbeID] = true
	}
	if spec != nil {
		for _, criterion := range spec.Criteria {
			for _, probe := range criterion.Probes {
				if !coveredProbes[probe.ID] {
					return fmt.Errorf("probe %s has no execution step", probe.ID)
				}
				for required := range requiredEvidenceByProbe[probe.ID] {
					if !producedByProbe[probe.ID][required] {
						return fmt.Errorf("probe %s execution steps do not produce required evidence type %q", probe.ID, required)
					}
				}
			}
		}
	}
	return nil
}

func validateExecutionStepType(step VerificationPlanStep) error {
	switch strings.TrimSpace(step.Type) {
	case "shell":
		if strings.TrimSpace(step.Command) == "" {
			return fmt.Errorf("execution step %s shell command required", step.ID)
		}
		if step.ExpectedExitCode == nil {
			return fmt.Errorf("execution step %s expected_exit_code required for shell", step.ID)
		}
	case "http":
		if strings.TrimSpace(step.Method) == "" || strings.TrimSpace(step.URL) == "" {
			return fmt.Errorf("execution step %s http method and url required", step.ID)
		}
		if step.ExpectedStatus == 0 {
			return fmt.Errorf("execution step %s expected_status required for http", step.ID)
		}
	case "playwright":
		if strings.TrimSpace(step.Script) == "" {
			return fmt.Errorf("execution step %s playwright script required", step.ID)
		}
	case "db_query":
		if strings.TrimSpace(step.Query) == "" || strings.TrimSpace(step.Path) == "" {
			return fmt.Errorf("execution step %s db_query path and query required", step.ID)
		}
	case "file_assertion":
		if strings.TrimSpace(step.Path) == "" || strings.TrimSpace(step.Assertion) == "" {
			return fmt.Errorf("execution step %s file_assertion path and assertion required", step.ID)
		}
	default:
		return fmt.Errorf("execution step %s type must be shell, http, playwright, db_query, or file_assertion", step.ID)
	}
	return nil
}

// requiresCommandMetadata returns true for evidence types that should be backed
// by artifact command metadata (i.e. the artifact registry should record which
// command produced the artifact).
func requiresCommandMetadata(artifactType string) bool {
	switch strings.ToLower(strings.TrimSpace(artifactType)) {
	case "cli_transcript", "test_output", "shell_output", "command_output":
		return true
	default:
		return false
	}
}

// commandMatches checks whether the evidence's claimed command is consistent
// with the artifact's registered command. It allows the evidence command to be
// a prefix or contain the artifact command (to tolerate wrapper differences),
// and vice versa. It also tolerates shell variable expansion differences
// where the evidence uses variables like $VAR and the artifact has expanded values.
func commandMatches(evidenceCommand, artifactCommand string) bool {
	evidenceCommand = strings.TrimSpace(evidenceCommand)
	artifactCommand = strings.TrimSpace(artifactCommand)
	if evidenceCommand == artifactCommand {
		return true
	}
	// Allow partial match: one command contains the other
	if strings.Contains(evidenceCommand, artifactCommand) {
		return true
	}
	if strings.Contains(artifactCommand, evidenceCommand) {
		return true
	}
	// Tolerate shell variable expansion: strip shell variables from evidence
	// and check if the structure matches the artifact command
	evidenceSansVars := stripShellVariables(evidenceCommand)
	artifactSansVars := stripShellVariables(artifactCommand)
	if evidenceSansVars == artifactSansVars {
		return true
	}
	if strings.Contains(artifactSansVars, evidenceSansVars) || strings.Contains(evidenceSansVars, artifactSansVars) {
		return true
	}
	return false
}

// stripShellVariables removes $VAR and ${VAR} patterns and replaces quoted string
// contents with a placeholder, so structural comparison works across variable expansion.
func stripShellVariables(cmd string) string {
	// Replace $VARNAME with placeholder
	cmd = regexp.MustCompile(`\$\{?[A-Za-z_][A-Za-z0-9_]*\}?`).ReplaceAllString(cmd, "VAR")
	// Replace quoted string contents with placeholder
	cmd = regexp.MustCompile(`"[^"]*"`).ReplaceAllString(cmd, "STR")
	cmd = regexp.MustCompile(`'[^']*'`).ReplaceAllString(cmd, "STR")
	return cmd
}

func criteriaByID(spec *AcceptanceSpec) map[string]AcceptanceCriterion {
	criteria := map[string]AcceptanceCriterion{}
	if spec == nil {
		return criteria
	}
	for _, c := range spec.Criteria {
		criteria[c.ID] = c
	}
	return criteria
}
