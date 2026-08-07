// Package architecture evaluates checked-in dependency constraints over graph facts.
package architecture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
)

const (
	Schema       = "weave.architecture/v1"
	ReportSchema = "weave.architecture-result/v1"
	SARIFSchema  = "https://json.schemastore.org/sarif-2.1.0.json"
	maxConfig    = 1 << 20
)

var ErrNotConfigured = errors.New("architecture configuration not found")

type Config struct {
	Schema string  `json:"schema"`
	Layers []Layer `json:"layers"`
	Rules  []Rule  `json:"rules"`
}

// Layer is a union of path, semantic-unit, and stable-symbol selectors.
// Patterns use path.Match syntax; a trailing /** selects a path prefix.
type Layer struct {
	ID      string   `json:"id"`
	Paths   []string `json:"paths,omitempty"`
	Units   []string `json:"units,omitempty"`
	Symbols []string `json:"symbols,omitempty"`
}

type Rule struct {
	ID      string           `json:"id"`
	Action  string           `json:"action"`
	From    string           `json:"from"`
	To      string           `json:"to"`
	Kinds   []graph.EdgeKind `json:"kinds"`
	Message string           `json:"message,omitempty"`
}

type Violation struct {
	RuleID   string         `json:"rule_id"`
	Message  string         `json:"message"`
	From     string         `json:"from"`
	To       string         `json:"to"`
	Kind     graph.EdgeKind `json:"kind"`
	Document string         `json:"document,omitempty"`
	Range    graph.Range    `json:"range"`
	Provider string         `json:"provider"`
	Evidence graph.Evidence `json:"evidence"`
}

type Report struct {
	Schema     string      `json:"schema"`
	Violations []Violation `json:"violations"`
}

func Load(file string) (Config, error) {
	input, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotConfigured
	}
	if err != nil {
		return Config{}, fmt.Errorf("open architecture configuration: %w", err)
	}
	defer input.Close()
	limited := io.LimitReader(input, maxConfig+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return Config{}, fmt.Errorf("read architecture configuration: %w", err)
	}
	if len(content) > maxConfig {
		return Config{}, fmt.Errorf("architecture configuration exceeds %d bytes", maxConfig)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode architecture configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Config{}, fmt.Errorf("decode architecture configuration: trailing JSON value")
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) validate() error {
	if config.Schema != Schema {
		return fmt.Errorf("architecture schema is %q, want %q", config.Schema, Schema)
	}
	if len(config.Layers) > 256 || len(config.Rules) > 4096 {
		return fmt.Errorf("architecture configuration exceeds layer or rule bounds")
	}
	layers := map[string]bool{}
	for _, layer := range config.Layers {
		if layer.ID == "" || layers[layer.ID] {
			return fmt.Errorf("layer ID %q is empty or duplicated", layer.ID)
		}
		layers[layer.ID] = true
		patterns := append(append(append([]string{}, layer.Paths...), layer.Units...), layer.Symbols...)
		if len(patterns) == 0 || len(patterns) > 256 {
			return fmt.Errorf("layer %q must have between 1 and 256 selectors", layer.ID)
		}
		for _, pattern := range patterns {
			if err := validatePattern(pattern); err != nil {
				return fmt.Errorf("layer %q: %w", layer.ID, err)
			}
		}
	}
	rules := map[string]bool{}
	for _, rule := range config.Rules {
		if rule.ID == "" || rules[rule.ID] {
			return fmt.Errorf("rule ID %q is empty or duplicated", rule.ID)
		}
		rules[rule.ID] = true
		if rule.Action != "allow" && rule.Action != "forbid" {
			return fmt.Errorf("rule %q action must be allow or forbid", rule.ID)
		}
		if !layers[rule.From] || !layers[rule.To] {
			return fmt.Errorf("rule %q references unknown layer", rule.ID)
		}
		if len(rule.Kinds) == 0 || len(rule.Kinds) > 32 {
			return fmt.Errorf("rule %q must select between 1 and 32 edge kinds", rule.ID)
		}
		for _, kind := range rule.Kinds {
			if !graph.IsEdgeKind(kind) {
				return fmt.Errorf("rule %q has unknown edge kind %q", rule.ID, kind)
			}
		}
	}
	return nil
}

func validatePattern(pattern string) error {
	if pattern == "" || strings.Contains(pattern, "\\") || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "../") {
		return fmt.Errorf("invalid portable pattern %q", pattern)
	}
	if strings.HasSuffix(pattern, "/**") {
		pattern = strings.TrimSuffix(pattern, "/**")
	}
	if _, err := path.Match(pattern, pattern); err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	return nil
}

// Check evaluates every edge in canonical order. Forbid rules directly reject
// matching edges. Applicable allow rules form an OR allowlist for source/kind.
func Check(config Config, snapshot graph.Snapshot) Report {
	layers := map[string]Layer{}
	for _, layer := range config.Layers {
		layers[layer.ID] = layer
	}
	symbols := map[string]graph.Symbol{}
	documents := map[string]graph.Document{}
	for _, symbol := range snapshot.Symbols {
		symbols[symbol.ID] = symbol
	}
	for _, document := range snapshot.Documents {
		documents[document.ID] = document
	}
	report := Report{Schema: ReportSchema}
	for _, edge := range snapshot.Edges {
		from := symbols[edge.From]
		to := symbols[edge.To]
		var allows []Rule
		for _, rule := range config.Rules {
			if !slices.Contains(rule.Kinds, edge.Kind) || !matches(layers[rule.From], from, documents[from.DocumentID], edge.From) {
				continue
			}
			if rule.Action == "allow" {
				allows = append(allows, rule)
				continue
			}
			if matches(layers[rule.To], to, documents[to.DocumentID], edge.To) {
				report.Violations = append(report.Violations, violation(rule, edge, documents[edge.DocumentID], "forbidden dependency"))
			}
		}
		if len(allows) > 0 {
			allowed := false
			for _, rule := range allows {
				allowed = allowed || matches(layers[rule.To], to, documents[to.DocumentID], edge.To)
			}
			if !allowed {
				report.Violations = append(report.Violations, violation(allows[0], edge, documents[edge.DocumentID], "dependency is outside the allowed target layers"))
			}
		}
	}
	slices.SortFunc(report.Violations, func(a, b Violation) int {
		if a.RuleID != b.RuleID {
			return strings.Compare(a.RuleID, b.RuleID)
		}
		if a.Document != b.Document {
			return strings.Compare(a.Document, b.Document)
		}
		if a.From != b.From {
			return strings.Compare(a.From, b.From)
		}
		return strings.Compare(a.To, b.To)
	})
	return report
}

func matches(layer Layer, symbol graph.Symbol, document graph.Document, fallbackID string) bool {
	for _, pattern := range layer.Paths {
		if match(pattern, document.Path) {
			return true
		}
	}
	for _, pattern := range layer.Units {
		if match(pattern, symbol.UnitID) {
			return true
		}
	}
	for _, pattern := range layer.Symbols {
		if match(pattern, fallbackID) || match(pattern, symbol.StableName) {
			return true
		}
	}
	return false
}

func match(pattern, value string) bool {
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "**")
		return strings.HasPrefix(value, prefix)
	}
	ok, _ := path.Match(pattern, value)
	return ok
}

func violation(rule Rule, edge graph.Edge, document graph.Document, fallback string) Violation {
	message := rule.Message
	if message == "" {
		message = fallback
	}
	return Violation{RuleID: rule.ID, Message: message, From: edge.From, To: edge.To, Kind: edge.Kind, Document: document.Path, Range: edge.Range, Provider: edge.Provider, Evidence: edge.Evidence}
}

type SARIFLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []SARIFRun `json:"runs"`
}
type SARIFRun struct {
	Tool        SARIFTool       `json:"tool"`
	Results     []SARIFResult   `json:"results"`
	Invocations []SARIFInvocation `json:"invocations,omitempty"`
}
type SARIFInvocation struct {
	ExecutionSuccessful       bool                `json:"executionSuccessful"`
	ToolExecutionNotifications []SARIFNotification `json:"toolExecutionNotifications,omitempty"`
}
type SARIFNotification struct {
	Level      string                 `json:"level"`
	Message    SARIFMessage           `json:"message"`
	Descriptor SARIFDescriptorReference `json:"descriptor"`
}
type SARIFDescriptorReference struct { ID string `json:"id"` }
type SARIFTool struct {
	Driver SARIFDriver `json:"driver"`
}
type SARIFDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri"`
	Rules          []SARIFRule `json:"rules"`
}
type SARIFRule struct {
	ID               string       `json:"id"`
	ShortDescription SARIFMessage `json:"shortDescription"`
}
type SARIFResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   SARIFMessage    `json:"message"`
	Locations []SARIFLocation `json:"locations,omitempty"`
}
type SARIFMessage struct {
	Text string `json:"text"`
}
type SARIFLocation struct {
	PhysicalLocation SARIFPhysical `json:"physicalLocation"`
}
type SARIFPhysical struct {
	ArtifactLocation SARIFArtifact `json:"artifactLocation"`
	Region           SARIFRegion   `json:"region"`
}
type SARIFArtifact struct {
	URI string `json:"uri"`
}
type SARIFRegion struct {
	StartLine   int32 `json:"startLine"`
	StartColumn int32 `json:"startColumn"`
	EndLine     int32 `json:"endLine,omitempty"`
	EndColumn   int32 `json:"endColumn,omitempty"`
}

func SARIF(config Config, report Report) SARIFLog {
	rules := make([]SARIFRule, len(config.Rules))
	for i, rule := range config.Rules {
		text := rule.Message
		if text == "" {
			text = rule.Action + " " + rule.From + " -> " + rule.To
		}
		rules[i] = SARIFRule{ID: rule.ID, ShortDescription: SARIFMessage{Text: text}}
	}
	slices.SortFunc(rules, func(a, b SARIFRule) int { return strings.Compare(a.ID, b.ID) })
	results := make([]SARIFResult, len(report.Violations))
	for i, item := range report.Violations {
		result := SARIFResult{RuleID: item.RuleID, Level: "error", Message: SARIFMessage{Text: item.Message}}
		if item.Document != "" {
			result.Locations = []SARIFLocation{{PhysicalLocation: SARIFPhysical{ArtifactLocation: SARIFArtifact{URI: item.Document}, Region: SARIFRegion{
				StartLine: item.Range.Start.Line + 1, StartColumn: item.Range.Start.Column + 1,
				EndLine: item.Range.End.Line + 1, EndColumn: item.Range.End.Column + 1,
			}}}}
		}
		results[i] = result
	}
	return SARIFLog{Version: "2.1.0", Schema: SARIFSchema, Runs: []SARIFRun{{Tool: SARIFTool{Driver: SARIFDriver{Name: "weave", InformationURI: "https://github.com/TheFellow/weave", Rules: rules}}, Results: results}}}
}
