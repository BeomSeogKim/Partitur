// Package score compiles Partitur v0.2 scores into validated, defaulted
// canonical projections.
package score

// RuleID is the stable owner of a compiler diagnostic.
type RuleID string

const (
	RuleIngress RuleID = "score.ingress"
	RuleSchema  RuleID = "score.schema"

	Rule01 RuleID = "§2.1"
	Rule02 RuleID = "§2.2"
	Rule03 RuleID = "§2.3"
	Rule04 RuleID = "§2.4"
	Rule06 RuleID = "§2.6"
	Rule07 RuleID = "§2.7"
	Rule08 RuleID = "§2.8"
	Rule09 RuleID = "§2.9"
	Rule10 RuleID = "§2.10"
	Rule11 RuleID = "§2.11"
	Rule12 RuleID = "§2.12"
	Rule13 RuleID = "§2.13"
	Rule14 RuleID = "§2.14"
	Rule15 RuleID = "§2.15"
	Rule16 RuleID = "§2.16"
	Rule17 RuleID = "§2.17"
	Rule18 RuleID = "§2.18"
	Rule19 RuleID = "§2.19"
)

// Diagnostic describes one source defect. Pointer is an RFC 6901 pointer.
// Detail is a stable machine-readable code, not rendered prose.
type Diagnostic struct {
	Rule    RuleID
	Pointer string
	Detail  string
}

// Score is a validated, defaulted score. Its representation is private so
// callers cannot mutate the value from which projections and hashes are made.
type Score struct {
	document document
}

// PartView is a defensive read view of an effective part.
type PartView struct {
	ID           string
	Capabilities []string
	ReadOnly     bool
}

// MovementView is a defensive read view of the fields needed for cast and
// fail-closed validation.
type MovementView struct {
	ID         string
	PartID     string
	Phase      string
	Grants     []string
	MayPropose bool
}

// PolicyView is a defensive read view of effective score policy.
type PolicyView struct {
	AllowedPaths       []string
	SideEffects        []string
	ActiveWallClockMin int64
	RetriesPerMovement int64
	AmendmentAuto      string
}

type document struct {
	Version       string
	Name          string
	Revision      float64
	Status        string
	Goal          string
	Context       *string
	Draft         *draft
	OpenQuestions []question
	Verification  *verification
	Parts         map[string]part
	Movements     []movement
	Policy        policy
	Extensions    map[string]any

	PartsComplete          bool
	MovementsComplete      bool
	MovementIDsComplete    bool
	MovementPhasesComplete bool
	OutputsComplete        bool
	DefaultsApplied        bool
	InvalidPointers        map[string]struct{}
}

type draft struct {
	InterviewMovement string
}

type question struct {
	SourceIndex int
	ID          string
	Question    string
	Resolution  *string
	Waived      *bool
}

type verification struct {
	Expectation   *expectation
	FinalMovement *string
}

type expectation struct {
	Intent    *string
	ApplyGate *applyGate
}

type applyGate struct {
	Require    []string
	RequireSet bool
	Waived     *bool
	Reason     *string
	Predicates []string
}

type part struct {
	Capabilities []string
	ReadOnly     bool
	Valid        bool
}

type movement struct {
	SourceIndex     int
	ID              string
	Phase           *string
	PartID          string
	Needs           []string
	Grants          []string
	Instruction     string
	Inputs          []string
	Outputs         []output
	OutputsComplete bool
	MayPropose      bool
	MayProposeSet   bool
	Acceptance      acceptance
}

type output struct {
	SourceIndex int
	ID          string
	Kind        string
}

type acceptance struct {
	Hard      []hardCriterion
	Review    []reviewCriterion
	HumanGate string
}

type hardCriterion struct {
	SourceIndex  int
	ID           string
	IDSet        bool
	Run          []string
	Artifact     *string
	TimeoutMin   *float64
	ExpectedHash *string
}

type reviewCriterion struct {
	SourceIndex int
	ID          string
	IDSet       bool
	Findings    string
	Rubric      []string
}

type policy struct {
	AllowedPaths []string
	SideEffects  []string
	Budget       budget
	Amendment    amendment
}

type budget struct {
	ActiveWallClockMin float64
	RetriesPerMovement float64
}

type amendment struct {
	Auto string
}
