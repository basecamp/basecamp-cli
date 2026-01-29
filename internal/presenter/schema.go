// Package presenter provides schema-aware rendering for Basecamp entities.
// It sits between commands and the output renderer, using declarative YAML
// schemas to transform generic data into human-centered terminal output.
package presenter

// EntitySchema describes how a Basecamp entity wants to be presented.
// Schemas are declarative metadata loaded from YAML files.
type EntitySchema struct {
	Entity    string                  `yaml:"entity"`
	Kind      string                  `yaml:"kind"`
	TypeKey   string                  `yaml:"type_key"`
	Identity  Identity                `yaml:"identity"`
	Headline  map[string]HeadlineSpec `yaml:"headline"`
	Fields    map[string]FieldSpec    `yaml:"fields"`
	Views     ViewSpecs               `yaml:"views"`
	Relations map[string]Relationship `yaml:"relationships"`
	Actions   []Affordance            `yaml:"affordances"`
}

// Identity identifies the entity's label and ID fields.
type Identity struct {
	Label string `yaml:"label"`
	ID    string `yaml:"id"`
	Icon  string `yaml:"icon"`
}

// HeadlineSpec defines a headline template, optionally conditional.
type HeadlineSpec struct {
	Template string `yaml:"template"`
}

// FieldSpec describes how a single field should be presented.
type FieldSpec struct {
	Role        string            `yaml:"role"`
	Emphasis    string            `yaml:"emphasis"`
	Format      string            `yaml:"format"`
	Collapse    bool              `yaml:"collapse"`
	Labels      map[string]string `yaml:"labels"`
	WhenOverdue string            `yaml:"when_overdue"`
}

// ViewSpecs declares which fields appear per presentation context.
type ViewSpecs struct {
	List    ListView    `yaml:"list"`
	Detail  DetailView  `yaml:"detail"`
	Compact CompactView `yaml:"compact"`
}

// ListView configures the table/list presentation.
type ListView struct {
	Columns []string `yaml:"columns"`
}

// DetailView configures the single-entity detail presentation.
type DetailView struct {
	Sections []DetailSection `yaml:"sections"`
}

// DetailSection groups fields under an optional heading.
type DetailSection struct {
	Heading string   `yaml:"heading"`
	Fields  []string `yaml:"fields"`
}

// CompactView configures a minimal inline presentation.
type CompactView struct {
	Show   []string `yaml:"show"`
	Inline bool     `yaml:"inline"`
}

// Relationship describes a connection to another entity.
type Relationship struct {
	Entity      string `yaml:"entity"`
	Via         string `yaml:"via"`
	Label       string `yaml:"label"`
	Cardinality string `yaml:"cardinality"`
}

// Affordance is a templated CLI action the user can take.
type Affordance struct {
	Action string `yaml:"action"`
	Cmd    string `yaml:"cmd"`
	Label  string `yaml:"label"`
	When   string `yaml:"when"`
}
